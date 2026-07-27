import { randomUUID } from "node:crypto";
import {
  link,
  mkdir,
  open,
  readFile,
  rename,
  stat,
  unlink,
} from "node:fs/promises";
import { hostname } from "node:os";
import { dirname, resolve } from "node:path";
import { isDeepStrictEqual } from "node:util";

const EMPTY_DOCUMENT = Object.freeze({
  version: 2,
  attempt: null,
  outbox: [],
  game: {
    session_id: "",
    next_sequence: 1,
    pending_turn: null,
    preference: "",
    applied_action_ids: [],
  },
});
const NO_CHANGE = Symbol("no-change");

export class StoryWorkflowStore {
  constructor(path) {
    this.path = path;
    this.document = structuredClone(EMPTY_DOCUMENT);
    this.committing = false;
    this.lockReleaseUncertain = false;
    this.publicationUncertain = false;
    this.settlementDraft = null;
  }

  get game() {
    return this.document.game;
  }

  hasPendingRinWork() {
    return Boolean(
      this.document.attempt ||
      this.document.game.pending_turn ||
      this.document.outbox.length,
    );
  }

  async load() {
    if (this.committing) {
      throw new Error("cannot reload during a story save mutation");
    }
    if (this.publicationUncertain) {
      await this.reconcileUncertainPublication();
      return this;
    }
    if (this.lockReleaseUncertain) {
      await this.reconcileUncertainLockRelease();
      return this;
    }
    this.document = await readDocument(this.path);
    return this;
  }

  async rememberPreference(preference) {
    if (preference !== "tea" && preference !== "coffee") {
      throw new Error("preference must be tea or coffee");
    }
    await this.commit((next) => {
      next.game.preference = preference;
    });
  }

  async ensureSessionId(candidate) {
    await this.commit((next) => {
      if (!next.game.session_id) {
        next.game.session_id = candidate;
      } else if (next.game.session_id !== candidate) {
        throw new Error("story save is already bound to another Session");
      } else {
        return NO_CHANGE;
      }
    });
    return this.document.game.session_id;
  }

  async beginRinTurn(preference, requestedSequence) {
    if (preference !== "tea" && preference !== "coffee") {
      throw new Error("preference must be tea or coffee");
    }
    let turn;
    await this.commit((next) => {
      if (next.game.pending_turn) {
        turn = clone(next.game.pending_turn);
        return NO_CHANGE;
      }
      const sequence = requestedSequence ?? next.game.next_sequence;
      if (!Number.isSafeInteger(sequence) || sequence < 1 ||
          sequence !== next.game.next_sequence) {
        throw new Error("turn sequence does not match the durable game save");
      }
      next.game.preference = preference;
      next.game.pending_turn = { sequence, preference };
      next.game.next_sequence = sequence + 1;
      turn = clone(next.game.pending_turn);
    });
    return turn;
  }

  async applyBaselineAction(action) {
    await this.commit((next) => {
      next.game.applied_action_ids.push(action.id);
    });
  }

  async loadProposalAttempt() {
    return clone(this.document.attempt);
  }

  async createProposalAttempt(attempt) {
    let created = false;
    await this.commit((next) => {
      if (!next.attempt) {
        next.attempt = clone(attempt);
        created = true;
      } else {
        return NO_CHANGE;
      }
    });
    return created;
  }

  async saveProposalAttempt(attempt) {
    await this.commit((next) => {
      next.attempt = clone(attempt);
    });
  }

  async settleProposalAttempt({ attempt, report, apply }) {
    // apply mutates only the draft through recordRinAction. The game effect,
    // Outbox entry, and cleared Attempt are then published by one replacement.
    await this.commit(async (next) => {
      if (next.attempt?.operation_id !== attempt.operation_id) {
        throw new Error("Proposal Attempt identity changed before settlement");
      }
      this.settlementDraft = next;
      try {
        await apply();
      } finally {
        this.settlementDraft = null;
      }
      next.outbox.push({
        key: report.request_id,
        report: clone(report),
      });
      next.attempt = null;
      next.game.pending_turn = null;
    });
  }

  recordRinAction(action) {
    if (!this.settlementDraft) {
      throw new Error("Rin action must be recorded inside Proposal settlement");
    }
    this.settlementDraft.game.applied_action_ids.push(action.id);
  }

  async listOutcomeReports() {
    if (this.committing) {
      throw new Error("cannot list Outcome Reports during a story save mutation");
    }
    if (this.publicationUncertain || this.lockReleaseUncertain) {
      throw new Error("reload the story save before draining its Outcome Outbox");
    }
    await this.makeDirectoryTreeDurable(dirname(this.path));
    const release = await this.acquireSaveLock();
    let reports;
    let failure;
    try {
      this.document = await readDocument(this.path);
      reports = clone(this.document.outbox);
    } catch (error) {
      failure = error;
    }
    try {
      await release();
    } catch (error) {
      this.lockReleaseUncertain = true;
      if (!failure) failure = error;
    }
    if (failure) throw failure;
    return reports;
  }

  async acknowledgeOutcome(entry) {
    await this.commit((next) => {
      const index = next.outbox.findIndex((item) => item.key === entry.key);
      if (index < 0) throw new Error("Outcome Outbox entry disappeared");
      if (!isDeepStrictEqual(next.outbox[index], entry)) {
        throw new Error("Outcome Outbox entry changed before acknowledgement");
      }
      next.outbox.splice(index, 1);
    });
  }

  async commit(mutate) {
    if (this.publicationUncertain) {
      throw new Error(
        "story save publication is uncertain; reload before another mutation",
      );
    }
    if (this.lockReleaseUncertain) {
      throw new Error(
        "story save lock release is uncertain; reload before another mutation",
      );
    }
    if (this.committing) {
      throw new Error("concurrent story save mutation is not allowed");
    }
    this.committing = true;
    let release;
    let failure;
    try {
      await this.makeDirectoryTreeDurable(dirname(this.path));
      release = await this.acquireSaveLock();
      this.document = await readDocument(this.path);
      const next = clone(this.document);
      const disposition = await mutate(next);
      validateDocument(next);
      if (disposition === NO_CHANGE) {
        this.document = next;
      } else {
        try {
          await this.publish(next, true);
        } catch (error) {
          if (error instanceof StoryPublicationUncertainError) {
            // Rename already made this the only defensible in-process view.
            // Block later writes until load() reconciles it with the filesystem.
            this.document = next;
            this.publicationUncertain = true;
          }
          throw error;
        }
        this.document = next;
      }
    } catch (error) {
      failure = error;
    }
    try {
      if (release) await release();
    } catch (error) {
      this.lockReleaseUncertain = true;
    } finally {
      this.committing = false;
    }
    if (failure) {
      throw failure;
    }
  }

  async acquireSaveLock() {
    return acquireStorySaveLock(this.path);
  }

  async releaseAfterReconciliation(release, failure) {
    try {
      await release();
    } catch (error) {
      this.lockReleaseUncertain = true;
      if (!failure) failure = error;
    }
    if (failure) {
      throw failure;
    }
  }

  async reconcileUncertainLockRelease() {
    await this.makeDirectoryTreeDurable(dirname(this.path));
    const release = await this.acquireSaveLock();
    let failure;
    try {
      this.document = await readDocument(this.path);
      this.lockReleaseUncertain = false;
    } catch (error) {
      failure = error;
    }
    await this.releaseAfterReconciliation(release, failure);
  }

  async publish(document, directoryPrepared = false) {
    const directory = dirname(this.path);
    if (!directoryPrepared) {
      await this.makeDirectoryTreeDurable(directory);
    }
    const temporary = `${this.path}.${process.pid}.${randomUUID()}.tmp`;
    let created = false;
    try {
      const handle = await open(temporary, "wx");
      created = true;
      try {
        await handle.writeFile(`${JSON.stringify(document)}\n`, "utf8");
        await handle.sync();
      } finally {
        await handle.close();
      }
      await rename(temporary, this.path);
      created = false;
      try {
        await this.fencePublishedEntry();
      } catch (error) {
        throw new StoryPublicationUncertainError(error);
      }
    } finally {
      if (created) {
        await unlink(temporary).catch(() => {});
      }
    }
  }

  async makeDirectoryTreeDurable(directory) {
    if (process.platform === "win32") {
      await mkdir(directory, { recursive: true });
      return;
    }
    await makeDirectoryTreeSynced(directory, (path) => this.syncDirectory(path));
  }

  async syncDirectory(path) {
    await syncPath(path, "r");
  }

  async fencePublishedEntry() {
    if (process.platform === "win32") {
      // Node cannot portably open a Windows directory for FlushFileBuffers.
      // Reopen the renamed file with write access and flush that handle instead.
      await syncPath(this.path, "r+");
      return;
    }
    await this.syncDirectory(dirname(this.path));
  }

  async reconcileUncertainPublication() {
    await this.makeDirectoryTreeDurable(dirname(this.path));
    const release = await this.acquireSaveLock();
    let failure;
    try {
      const document = await readDocument(this.path);
      try {
        await this.fencePublishedEntry();
      } catch (error) {
        throw new StoryPublicationUncertainError(error);
      }
      this.document = document;
      this.publicationUncertain = false;
    } catch (error) {
      failure = error;
    }
    await this.releaseAfterReconciliation(release, failure);
  }
}

function clone(value) {
  return value == null ? value : structuredClone(value);
}

function validateDocument(value) {
  if (!value || value.version !== 2 || !Array.isArray(value.outbox) ||
      !value.game || !Array.isArray(value.game.applied_action_ids) ||
      typeof value.game.session_id !== "string" ||
      !Number.isSafeInteger(value.game.next_sequence) ||
      value.game.next_sequence < 1) {
    throw new Error("story save is malformed");
  }
}

async function readDocument(path) {
  try {
    const parsed = JSON.parse(await readFile(path, "utf8"));
    validateDocument(parsed);
    return parsed;
  } catch (error) {
    if (error?.code === "ENOENT") {
      return structuredClone(EMPTY_DOCUMENT);
    }
    throw error;
  }
}

async function makeDirectoryTreeSynced(path, syncDirectory) {
  const target = resolve(path);
  const missing = [];
  let boundary = target;
  while (true) {
    try {
      const info = await stat(boundary);
      if (!info.isDirectory()) {
        throw new Error(`${boundary} is not a directory`);
      }
      break;
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
      missing.push(boundary);
      const parent = dirname(boundary);
      if (parent === boundary) {
        throw new Error(`no existing parent for ${target}`);
      }
      boundary = parent;
    }
  }
  // Retry the nearest existing boundary first. It may have been created by a
  // previous attempt whose parent fence failed.
  await syncDirectory(dirname(boundary));
  for (let index = missing.length - 1; index >= 0; index--) {
    const directory = missing[index];
    try {
      await mkdir(directory);
    } catch (error) {
      if (error?.code !== "EEXIST" || !(await stat(directory)).isDirectory()) {
        throw error;
      }
    }
    await syncDirectory(dirname(directory));
  }
}

async function syncPath(path, flags) {
  const handle = await open(path, flags);
  try {
    await handle.sync();
  } finally {
    await handle.close();
  }
}

async function acquireStorySaveLock(path) {
  const lockPath = `${path}.lock`;
  const token = randomUUID();
  const candidate = `${lockPath}.${process.pid}.${token}.candidate`;
  const owner = {
    version: 1,
    host: hostname(),
    pid: process.pid,
    token,
  };
  try {
    const handle = await open(candidate, "wx");
    try {
      await handle.writeFile(`${JSON.stringify(owner)}\n`, "utf8");
      await handle.sync();
    } finally {
      await handle.close();
    }
  } catch (error) {
    await unlink(candidate).catch(() => {});
    throw error;
  }
  try {
    await link(candidate, lockPath);
  } catch (error) {
    await unlink(candidate).catch(() => {});
    throw storySaveBusy(error);
  }
  await unlink(candidate).catch(() => {});
  return async () => {
    let current;
    try {
      current = JSON.parse(await readFile(lockPath, "utf8"));
    } catch (error) {
      throw storySaveBusy(error);
    }
    if (current?.token !== token) {
      throw storySaveBusy(new Error("story save lock ownership changed"));
    }
    await unlink(lockPath);
  };
}

function storySaveBusy(cause) {
  const error = new Error(
    "story save is busy or has an unrecoverable lock; do not run two writers",
    { cause },
  );
  error.code = "story_save_busy";
  return error;
}

class StoryPublicationUncertainError extends Error {
  constructor(cause) {
    super(
      "story save was renamed but its durability fence failed; reload before retry",
      { cause },
    );
    this.name = "StoryPublicationUncertainError";
    this.code = "story_publication_uncertain";
  }
}

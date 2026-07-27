import { randomUUID } from "node:crypto";
import { mkdir, open, readFile, rename, stat, unlink } from "node:fs/promises";
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

export class StoryWorkflowStore {
  constructor(path) {
    this.path = path;
    this.document = structuredClone(EMPTY_DOCUMENT);
    this.committing = false;
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
    this.document = structuredClone(EMPTY_DOCUMENT);
    try {
      const parsed = JSON.parse(await readFile(this.path, "utf8"));
      validateDocument(parsed);
      this.document = parsed;
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
    }
    this.publicationUncertain = false;
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
    if (!this.document.game.session_id) {
      await this.commit((next) => {
        next.game.session_id = candidate;
      });
    } else if (this.document.game.session_id !== candidate) {
      throw new Error("story save is already bound to another Session");
    }
    return this.document.game.session_id;
  }

  async beginRinTurn(preference, requestedSequence) {
    if (this.document.game.pending_turn) {
      return clone(this.document.game.pending_turn);
    }
    if (preference !== "tea" && preference !== "coffee") {
      throw new Error("preference must be tea or coffee");
    }
    const sequence = requestedSequence ?? this.document.game.next_sequence;
    if (!Number.isSafeInteger(sequence) || sequence < 1 ||
        sequence !== this.document.game.next_sequence) {
      throw new Error("turn sequence does not match the durable game save");
    }
    await this.commit((next) => {
      next.game.preference = preference;
      next.game.pending_turn = { sequence, preference };
      next.game.next_sequence = sequence + 1;
    });
    return clone(this.document.game.pending_turn);
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
    if (this.document.attempt) return false;
    await this.commit((next) => {
      next.attempt = clone(attempt);
    });
    return true;
  }

  async saveProposalAttempt(attempt) {
    await this.commit((next) => {
      next.attempt = clone(attempt);
    });
  }

  async settleProposalAttempt({ attempt, report, apply }) {
    if (this.document.attempt?.operation_id !== attempt.operation_id) {
      throw new Error("Proposal Attempt identity changed before settlement");
    }
    // apply mutates only the draft through recordRinAction. The game effect,
    // Outbox entry, and cleared Attempt are then published by one replacement.
    await this.commit(async (next) => {
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
    return clone(this.document.outbox);
  }

  async acknowledgeOutcome(entry) {
    const index = this.document.outbox.findIndex((item) => item.key === entry.key);
    if (index < 0) throw new Error("Outcome Outbox entry disappeared");
    await this.commit((next) => {
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
    if (this.committing) {
      throw new Error("concurrent story save mutation is not allowed");
    }
    this.committing = true;
    try {
      const next = clone(this.document);
      await mutate(next);
      validateDocument(next);
      try {
        await this.publish(next);
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
    } finally {
      this.committing = false;
    }
  }

  async publish(document) {
    const directory = dirname(this.path);
    await this.makeDirectoryTreeDurable(directory);
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

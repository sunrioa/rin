import { randomUUID } from "node:crypto";
import { mkdir, open, readFile, rename, unlink } from "node:fs/promises";
import { dirname } from "node:path";

const EMPTY_DOCUMENT = Object.freeze({
  version: 1,
  attempt: null,
  outbox: [],
  game: {
    session_id: "",
    next_sequence: 1,
    pending_turn: null,
    preference: "",
    shown_action_ids: [],
  },
});

export class StoryWorkflowStore {
  constructor(path) {
    this.path = path;
    this.document = structuredClone(EMPTY_DOCUMENT);
    this.committing = false;
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
    try {
      const parsed = JSON.parse(await readFile(this.path, "utf8"));
      validateDocument(parsed);
      this.document = parsed;
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
    }
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
      next.game.shown_action_ids.push(action.id);
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
    this.settlementDraft.game.shown_action_ids.push(action.id);
  }

  async listOutcomeReports() {
    return clone(this.document.outbox);
  }

  async acknowledgeOutcome(entry) {
    const index = this.document.outbox.findIndex((item) => item.key === entry.key);
    if (index < 0) throw new Error("Outcome Outbox entry disappeared");
    await this.commit((next) => {
      next.outbox.splice(index, 1);
    });
  }

  async commit(mutate) {
    if (this.committing) {
      throw new Error("concurrent story save mutation is not allowed");
    }
    this.committing = true;
    try {
      const next = clone(this.document);
      await mutate(next);
      validateDocument(next);
      await this.publish(next);
      this.document = next;
    } finally {
      this.committing = false;
    }
  }

  async publish(document) {
    await mkdir(dirname(this.path), { recursive: true });
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
    } finally {
      if (created) {
        await unlink(temporary).catch(() => {});
      }
    }
  }
}

function clone(value) {
  return value == null ? value : structuredClone(value);
}

function validateDocument(value) {
  if (!value || value.version !== 1 || !Array.isArray(value.outbox) ||
      !value.game || !Array.isArray(value.game.shown_action_ids) ||
      typeof value.game.session_id !== "string" ||
      !Number.isSafeInteger(value.game.next_sequence) ||
      value.game.next_sequence < 1) {
    throw new Error("story save is malformed");
  }
}

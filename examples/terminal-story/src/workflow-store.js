import { mkdir, open, readFile, rename } from "node:fs/promises";
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
    this.document.game.preference = preference;
    await this.flush();
  }

  async ensureSessionId(candidate) {
    if (!this.document.game.session_id) {
      this.document.game.session_id = candidate;
      await this.flush();
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
    this.document.game.preference = preference;
    this.document.game.pending_turn = { sequence, preference };
    this.document.game.next_sequence = sequence + 1;
    await this.flush();
    return clone(this.document.game.pending_turn);
  }

  async applyBaselineAction(action) {
    this.document.game.shown_action_ids.push(action.id);
    await this.flush();
  }

  async loadProposalAttempt() {
    return clone(this.document.attempt);
  }

  async createProposalAttempt(attempt) {
    if (this.document.attempt) return false;
    this.document.attempt = clone(attempt);
    await this.flush();
    return true;
  }

  async saveProposalAttempt(attempt) {
    this.document.attempt = clone(attempt);
    await this.flush();
  }

  async settleProposalAttempt({ attempt, commit, apply }) {
    if (this.document.attempt?.operation_id !== attempt.operation_id) {
      throw new Error("Proposal Attempt identity changed before settlement");
    }
    // apply mutates only this document. The game effect, Outbox entry, and
    // cleared Attempt are then published by one atomic file replacement.
    await apply();
    this.document.outbox.push({
      key: commit.request_id,
      commit: clone(commit),
    });
    this.document.attempt = null;
    this.document.game.pending_turn = null;
    await this.flush();
  }

  applyRinAction(action) {
    this.document.game.shown_action_ids.push(action.id);
  }

  async listOutcomeReports() {
    return clone(this.document.outbox);
  }

  async acknowledgeOutcome(entry) {
    const index = this.document.outbox.findIndex((item) => item.key === entry.key);
    if (index < 0) throw new Error("Outcome Outbox entry disappeared");
    this.document.outbox.splice(index, 1);
    await this.flush();
  }

  async flush() {
    await mkdir(dirname(this.path), { recursive: true });
    const temporary = `${this.path}.${process.pid}.${Date.now()}.tmp`;
    const handle = await open(temporary, "wx");
    try {
      await handle.writeFile(`${JSON.stringify(this.document)}\n`, "utf8");
      await handle.sync();
    } finally {
      await handle.close();
    }
    await rename(temporary, this.path);
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

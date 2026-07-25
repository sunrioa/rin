export const SDK_VERSION: "0.6.0";
export const PROTOCOL_VERSION: "rin.protocol/v1";
export const DEFAULT_BASE_URL: string;
export const DEFAULT_MAX_RESPONSE_BYTES: number;
export const TRANSFER_CONTROL_FRAME_MAX_BYTES: number;
export const TRANSFER_EVENT_FRAME_MAX_BYTES: number;
export const RIN_FEATURES: Readonly<{
  memoryArchive: "memory-archive-v1";
  beliefConflicts: "belief-conflicts-v1";
  goalCandidates: "goal-candidates-v1";
  actorActivity: "actor-activity-v1";
  arbitration: "arbitration-v1";
  outcomeReporting: "outcome-reporting-v1";
}>;
export const FEATURE_PRESETS: Readonly<{
  authoritative: readonly ["outcome-reporting-v1"];
  full: readonly string[];
}>;

export type RinObject = Record<string, unknown>;
export type FetchImplementation = typeof globalThis.fetch;

export interface RinClientOptions {
  token?: string;
  timeoutMs?: number;
  maxResponseBytes?: number;
  fetch?: FetchImplementation;
  now?: () => number;
  sleep?: (milliseconds: number) => Promise<void>;
}

export interface RinPollingOptions {
  deadlineMs?: number;
  intervalMs?: number;
}

export interface RinBinding {
  game_id: string;
  content_id: string;
  content_version: string;
  content_hash: string;
}

export interface RinTransferSink {
  write(chunk: Uint8Array): void | Promise<void>;
}

export type RinTransferSource =
  | ReadableStream<Uint8Array>
  | AsyncIterable<Uint8Array>;

export class RinError extends Error { readonly code: string; }
export class RinConfigurationError extends RinError {}
export class RinTransportError extends RinError {}
export class RinProtocolError extends RinError {}
export class RinAPIError extends RinError {
  readonly status: number;
  readonly field: string;
}

export function createRinId(
  prefix?: string,
  randomBytes?: (length: number) => Uint8Array,
): string;

export class RinClient {
  constructor(baseUrl?: string, options?: RinClientOptions);
  readonly baseUrl: string;
  health(): Promise<RinObject>;
  negotiateCapabilities(requiredFeatures?: readonly string[]): Promise<RinObject>;
  createSession(payload: RinObject): Promise<RinObject>;
  observe(payload: RinObject): Promise<RinObject>;
  propose(payload: RinObject): Promise<RinObject>;
  submitProposalJob(payload: RinObject): Promise<RinObject>;
  getProposalJob(jobId: string): Promise<RinObject>;
  cancelProposalJob(jobId: string): Promise<RinObject>;
  submitGenerationJob(payload: RinObject): Promise<RinObject>;
  getGenerationJob(jobId: string): Promise<RinObject>;
  cancelGenerationJob(jobId: string): Promise<RinObject>;
  /** Report an outcome the game already applied or rejected. */
  commit(payload: RinObject): Promise<RinObject>;
  /** Atomically report outcomes produced from one original world revision. */
  commitBatch(payload: RinObject): Promise<RinObject>;
  setActorActivity(payload: RinObject): Promise<RinObject>;
  arbitrate(payload: RinObject): Promise<RinObject>;
  state(payload: RinObject): Promise<RinObject>;
  snapshot(payload: RinObject): Promise<RinObject>;
  restore(payload: RinObject): Promise<RinObject>;
  /** Streams an NDJSON transfer into a caller-owned sink without closing it. */
  exportSession(
    payload: RinObject,
    sink: RinTransferSink | WritableStream<Uint8Array>,
  ): Promise<RinObject>;
  /** Streams an NDJSON transfer from a caller-owned source. */
  importSession(
    source: RinTransferSource,
    expectedBinding: RinBinding,
  ): Promise<RinObject>;
  timeline(payload: RinObject): Promise<RinObject>;
  replay(payload: RinObject): Promise<RinObject>;
  dueAgents(payload: RinObject): Promise<RinObject>;
  waitForProposal(jobId: string, options?: RinPollingOptions): Promise<RinObject>;
  waitForGeneration(jobId: string, options?: RinPollingOptions): Promise<RinObject>;
}

export const PROTOCOL_VERSION: "rin.protocol/v1";
export const DEFAULT_BASE_URL: string;
export const DEFAULT_MAX_RESPONSE_BYTES: number;

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

export class RinError extends Error { readonly code: string; }
export class RinConfigurationError extends RinError {}
export class RinTransportError extends RinError {}
export class RinProtocolError extends RinError {}
export class RinAPIError extends RinError {
  readonly status: number;
  readonly field: string;
}

export class RinClient {
  constructor(baseUrl?: string, options?: RinClientOptions);
  readonly baseUrl: string;
  health(): Promise<RinObject>;
  createSession(payload: RinObject): Promise<RinObject>;
  observe(payload: RinObject): Promise<RinObject>;
  propose(payload: RinObject): Promise<RinObject>;
  submitProposalJob(payload: RinObject): Promise<RinObject>;
  getProposalJob(jobId: string): Promise<RinObject>;
  cancelProposalJob(jobId: string): Promise<RinObject>;
  submitGenerationJob(payload: RinObject): Promise<RinObject>;
  getGenerationJob(jobId: string): Promise<RinObject>;
  cancelGenerationJob(jobId: string): Promise<RinObject>;
  commit(payload: RinObject): Promise<RinObject>;
  commitBatch(payload: RinObject): Promise<RinObject>;
  setActorActivity(payload: RinObject): Promise<RinObject>;
  arbitrate(payload: RinObject): Promise<RinObject>;
  state(payload: RinObject): Promise<RinObject>;
  snapshot(payload: RinObject): Promise<RinObject>;
  restore(payload: RinObject): Promise<RinObject>;
  timeline(payload: RinObject): Promise<RinObject>;
  replay(payload: RinObject): Promise<RinObject>;
  dueAgents(payload: RinObject): Promise<RinObject>;
  waitForProposal(jobId: string, options?: RinPollingOptions): Promise<RinObject>;
  waitForGeneration(jobId: string, options?: RinPollingOptions): Promise<RinObject>;
}

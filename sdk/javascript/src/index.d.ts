export const SDK_VERSION: "0.6.0";
export const PROTOCOL_VERSION: "rin.protocol/v1";
export const DEFAULT_BASE_URL: string;
export const DEFAULT_MAX_RESPONSE_BYTES: number;
export const INLINE_SNAPSHOT_MAX_BYTES: number;
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
export type RinFeature =
  | "memory-archive-v1"
  | "belief-conflicts-v1"
  | "goal-candidates-v1"
  | "actor-activity-v1"
  | "arbitration-v1"
  | "outcome-reporting-v1";
export type GoalStatus = "active" | "completed" | "released";

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

export interface BoundaryInput {
  id: string;
  description: string;
  trigger_tags?: string[] | null;
  response: "refuse" | "redirect" | "wait";
}

export interface GoalSeedInput {
  id: string;
  description: string;
  motivation?: string;
  priority: number;
  preferred_actions?: string[];
  progress?: number;
  target_progress: number;
  status: GoalStatus;
}

export interface ActorSeedInput {
  id: string;
  kind: string;
  display_name: string;
  traits?: string[];
  boundaries?: BoundaryInput[];
  goals?: GoalSeedInput[];
  metadata?: Record<string, string>;
  think_every_ticks: number;
  enabled?: boolean;
}

export interface ActionSpecInput {
  id: string;
  kind: string;
  description: string;
  target_ids?: string[];
  parameters?: Record<string, string>;
}

export interface CreateSessionRequest {
  protocol_version: typeof PROTOCOL_VERSION;
  request_id: string;
  session_id: string;
  binding: RinBinding;
  seed?: number;
  features?: RinFeature[];
  actors: ActorSeedInput[];
}

export interface ProposeRequest {
  protocol_version: typeof PROTOCOL_VERSION;
  session_id: string;
  request_id: string;
  actor_id: string;
  tick?: number;
  intent: string;
  tags?: string[];
  candidate_actions: ActionSpecInput[];
  candidate_goals?: GoalSeedInput[];
  urgent?: boolean;
}

export interface FactInput {
  subject_id: string;
  predicate: string;
  object: string;
  visibility?: string[];
  confidence?: number;
  source_event_id?: string;
  observed_tick?: 0;
}

export interface GoalUpdateInput {
  goal_id: string;
  progress_delta?: number;
  status?: GoalStatus;
}

export interface CommitRequest {
  protocol_version: typeof PROTOCOL_VERSION;
  session_id: string;
  request_id: string;
  proposal_id: string;
  event_id: string;
  tick?: number;
  accepted: boolean;
  outcome?: string;
  tags?: string[];
  facts?: FactInput[];
  goal_updates?: GoalUpdateInput[];
}

export interface MutationResult {
  session_id: string;
  revision: number;
  head_hash: string;
  duplicate: boolean;
  [additiveField: string]: unknown;
}

export interface ActionProposal {
  id: string;
  session_id: string;
  request_id: string;
  actor_id: string;
  tick: number;
  based_on_revision: number;
  based_on_head_hash: string;
  based_on_world_revision?: number;
  created_revision: number;
  action: ActionSpecInput;
  stance: "engage" | "refuse" | "redirect" | "wait";
  summary: string;
  rationale: string;
  policy_source?: string;
  recalled_memory_ids?: string[];
  goal_id?: string;
  boundary_id?: string;
  proposed_goal?: RinObject;
  status: "pending" | "accepted" | "rejected";
  outcome_event_id?: string;
  outcome_tick?: number;
  [additiveField: string]: unknown;
}

export interface ProposalResult {
  proposal: ActionProposal;
  duplicate: boolean;
  [additiveField: string]: unknown;
}

export interface HealthData {
  status: "ok";
  protocol_version: typeof PROTOCOL_VERSION;
  release_version: string;
  release_status: "preview" | "stable" | "deprecated";
  policy_mode: string;
  async_jobs: boolean;
  structured_generation: boolean;
  features: string[];
  [additiveField: string]: unknown;
}

export interface RinTransferSink {
  write(chunk: Uint8Array): void | Promise<void>;
}

export interface ProposalAttempt {
  version: 1;
  operation_id: string;
  request: ProposeRequest;
  job_id: string;
}

export interface ProposalAttemptStore {
  loadProposalAttempt(): Promise<ProposalAttempt | null>;
  /** Atomically creates the Attempt; returns false when one already exists. */
  createProposalAttempt(attempt: ProposalAttempt): Promise<boolean>;
  /** Updates only the matching Attempt when persisting its Job identity. */
  saveProposalAttempt(attempt: ProposalAttempt): Promise<void>;
  /**
   * Must atomically run apply, persist the applied marker and Commit in the
   * Outcome Outbox, and remove the matching Proposal Attempt.
   */
  settleProposalAttempt(input: {
    attempt: ProposalAttempt;
    proposal: ActionProposal;
    commit: CommitRequest;
    apply: () => void | Promise<void>;
  }): Promise<void>;
}

export interface OutcomeOutboxEntry {
  commit: CommitRequest;
  [durableMetadata: string]: unknown;
}

export interface OutcomeOutboxStore {
  listOutcomeReports(): Promise<OutcomeOutboxEntry[]>;
  /** Must durably remove only the exact entry that Rin acknowledged. */
  acknowledgeOutcome(entry: OutcomeOutboxEntry, result: MutationResult): Promise<void>;
}

export interface OpaqueSnapshotStore {
  putSnapshot(key: string, snapshot: Uint8Array): Promise<void>;
  getSnapshot(key: string): Promise<Uint8Array>;
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

export class ProposalAttemptCoordinator {
  constructor(client: RinClient, store: ProposalAttemptStore);
  begin(operationId: string, request: ProposeRequest): Promise<ProposalAttempt>;
  resume(options?: RinPollingOptions): Promise<{
    attempt: ProposalAttempt;
    proposal: ActionProposal;
    duplicate: boolean;
  }>;
  settle(
    attempt: ProposalAttempt,
    proposal: ActionProposal,
    commit: CommitRequest,
    apply: () => void | Promise<void>,
  ): Promise<void>;
}

export class OutcomeOutbox {
  constructor(client: RinClient, store: OutcomeOutboxStore);
  drain(): Promise<number>;
}

export class OpaqueSnapshotPersistence {
  constructor(store: OpaqueSnapshotStore);
  save(key: string, snapshot: RinObject): Promise<void>;
  load(key: string): Promise<RinObject>;
}

export function createRinId(
  prefix?: string,
  randomBytes?: (length: number) => Uint8Array,
): string;

export class RinClient {
  constructor(baseUrl?: string, options?: RinClientOptions);
  readonly baseUrl: string;
  health(): Promise<HealthData>;
  negotiateCapabilities(requiredFeatures?: readonly string[]): Promise<HealthData>;
  createSession(payload: CreateSessionRequest): Promise<MutationResult>;
  observe(payload: RinObject): Promise<RinObject>;
  propose(payload: ProposeRequest): Promise<ProposalResult>;
  submitProposalJob(payload: RinObject): Promise<RinObject>;
  getProposalJob(jobId: string): Promise<RinObject>;
  cancelProposalJob(jobId: string): Promise<RinObject>;
  submitGenerationJob(payload: RinObject): Promise<RinObject>;
  getGenerationJob(jobId: string): Promise<RinObject>;
  cancelGenerationJob(jobId: string): Promise<RinObject>;
  /** Report an outcome the game already applied or rejected. */
  commit(payload: CommitRequest): Promise<MutationResult>;
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

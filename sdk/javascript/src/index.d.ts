export const SDK_VERSION: "0.7.0";
export const PROTOCOL_VERSION: "rin.protocol/v2";
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
}>;
export const FEATURE_PRESETS: Readonly<{
  safeBaseline: readonly [];
  authoritative: readonly [];
  full: readonly string[];
}>;

export type RinObject = Record<string, unknown>;
export type FetchImplementation = typeof globalThis.fetch;
export type RinFeature =
  | "memory-archive-v1"
  | "belief-conflicts-v1"
  | "goal-candidates-v1"
  | "actor-activity-v1"
  | "arbitration-v1";
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

export type HostClock = "event" | "step" | "realtime";
export type DecisionMode = "sequential" | "simultaneous" | "asynchronous";
export type ActionRunStatus =
  | "queued"
  | "running"
  | "succeeded"
  | "failed"
  | "cancelled"
  | "interrupted"
  | "stale"
  | "outcome-unknown";

export interface Epoch {
  session_id: string;
  world_id: string;
  host: number;
  world: number;
  timeline: number;
}

export interface Timepoint {
  clock: HostClock;
  value: number;
}

export interface CapabilityRef {
  id: string;
  version: string;
}

export interface HostRef {
  namespace: string;
  type: string;
  key: string;
  ephemeral: boolean;
  epoch: Epoch;
}

export interface DecisionWindow {
  id: string;
  mode: DecisionMode;
  epoch: Epoch;
  observation_seq: number;
  opened_at: Timepoint;
  deadline: Timepoint;
  actor_ids: string[];
}

export interface ActionOfferInput {
  offer_id: string;
  decision_window_id: string;
  actor_id: string;
  capability: CapabilityRef;
  descriptor_digest: string;
  description: string;
  arguments: unknown;
  targets?: HostRef[];
  expected_epoch: Epoch;
  observation_seq: number;
  deadline: Timepoint;
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
  decision_window: DecisionWindow;
  offers: ActionOfferInput[];
  candidate_goals?: GoalSeedInput[];
  urgent?: boolean;
}

export interface ObserveRequest {
  protocol_version: typeof PROTOCOL_VERSION;
  session_id: string;
  request_id: string;
  event_id: string;
  tick?: number;
  observer_ids: string[];
  source: string;
  kind: string;
  summary: string;
  quote?: string;
  tags?: string[];
  importance: number;
  facts?: FactInput[];
  epoch: Epoch;
  observation_seq: number;
  payload?: {
    schema: { id: string; version: string; digest: string };
    data: unknown;
  };
  artifacts?: Array<{
    id: string;
    media_type: string;
    uri: string;
    sha256: string;
    size_bytes: number;
  }>;
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

export interface ActionInvocation {
  operation_id: string;
  offer_id: string;
  decision_window_id: string;
  actor_id: string;
  capability: CapabilityRef;
  descriptor_digest: string;
  arguments: unknown;
  targets?: HostRef[];
  expected_epoch: Epoch;
  observation_seq: number;
  deadline: Timepoint;
}

export interface ActionRun {
  operation_id: string;
  status: ActionRunStatus;
  progress_seq: number;
  progress: number;
  updated_at: Timepoint;
  message?: string;
}

export interface ActionOutcome {
  operation_id: string;
  status: Exclude<ActionRunStatus, "queued" | "running">;
  code?: string;
  summary: string;
  evidence?: HostRef[];
  epoch: Epoch;
  world_seq: number;
  occurred_at: Timepoint;
}

export interface ActionReport {
  proposal_id: string;
  event_id: string;
  decision: "accepted" | "rejected";
  invocation?: ActionInvocation;
  run?: ActionRun;
  outcome?: ActionOutcome;
  summary: string;
  tags?: string[];
  facts?: FactInput[];
  goal_updates?: GoalUpdateInput[];
}

export interface ReportActionRequest {
  protocol_version: typeof PROTOCOL_VERSION;
  session_id: string;
  request_id: string;
  tick?: number;
  report: ActionReport;
}

export interface BatchActionReportRequest {
  protocol_version: typeof PROTOCOL_VERSION;
  session_id: string;
  request_id: string;
  tick?: number;
  reports: ActionReport[];
}

export interface SessionRequest {
  protocol_version: typeof PROTOCOL_VERSION;
  session_id: string;
}

export interface ArchiveSessionRequest extends SessionRequest {
  request_id: string;
  expected_binding: RinBinding;
  expected_revision: number;
  expected_head_hash: string;
}

export interface DeleteSessionRequest extends ArchiveSessionRequest {
  archive_receipt_id: string;
  confirmation: string;
}

export interface SessionStats {
  session_id: string;
  lifecycle: "active" | "archived";
  revision: number;
  head_hash: string;
  event_count: number;
  bytes: {
    event_log: number;
    snapshots: number;
    checkpoints: number;
    indexes: number;
    other: number;
    total: number;
  };
  soft_limit_bytes: number;
  hard_limit_bytes: number;
  soft_limit_exceeded: boolean;
  hard_limit_exceeded: boolean;
  [additiveField: string]: unknown;
}

export interface ArchiveSessionResult {
  session_id: string;
  receipt_id: string;
  revision: number;
  head_hash: string;
  archived_at: string;
  duplicate: boolean;
  [additiveField: string]: unknown;
}

export interface DeleteSessionResult {
  session_id: string;
  receipt_id: string;
  deleted_at: string;
  duplicate: boolean;
  [additiveField: string]: unknown;
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
  decision_window: DecisionWindow;
  action: ActionOfferInput;
  stance: "engage" | "refuse" | "redirect" | "wait";
  summary: string;
  rationale: string;
  policy_source?: string;
  recalled_memory_ids?: string[];
  goal_id?: string;
  boundary_id?: string;
  proposed_goal?: RinObject;
  status: "pending" | "accepted" | "rejected";
  invocation?: ActionInvocation;
  run?: ActionRun;
  outcome?: ActionOutcome;
  last_report_event_id?: string;
  last_report_tick?: number;
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
  recommended_features: string[];
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

export type PendingTurn = ProposalAttempt;

export type HostDurabilityProfile =
  | "advisory"
  | "idempotent-action"
  | "transactional-action";

export const HOST_DURABILITY_PROFILES: Readonly<{
  advisory: "advisory";
  idempotentAction: "idempotent-action";
  transactionalAction: "transactional-action";
}>;

export interface HostDurabilityOptions {
  version?: number;
  profile?: HostDurabilityProfile;
  stableIdentity?: boolean;
  durableBeforeNetwork?: boolean;
  durableOutbox?: boolean;
  idempotentApply?: boolean;
  atomicApplyAndOutbox?: boolean;
}

export class HostDurability {
  constructor(options?: HostDurabilityOptions);
  readonly version: number;
  readonly profile: HostDurabilityProfile;
  readonly stableIdentity: boolean;
  readonly durableBeforeNetwork: boolean;
  readonly durableOutbox: boolean;
  readonly idempotentApply: boolean;
  readonly atomicApplyAndOutbox: boolean;
  require(requiredDurability: HostDurabilityProfile): void;
  static advisory(options?: HostDurabilityOptions): HostDurability;
  static idempotentAction(options?: HostDurabilityOptions): HostDurability;
  static transactionalAction(options?: HostDurabilityOptions): HostDurability;
}

export interface ProposalAttemptPersistence {
  loadProposalAttempt(): Promise<ProposalAttempt | null>;
  /** Atomically creates the Attempt; returns false when one already exists. */
  createProposalAttempt(attempt: ProposalAttempt): Promise<boolean>;
  /** Updates only the matching Attempt when persisting its Job identity. */
  saveProposalAttempt(attempt: ProposalAttempt): Promise<void>;
}

export interface ProposalAttemptStore extends ProposalAttemptPersistence {
  /**
   * Must atomically run apply, persist the applied marker and Action Report in the
   * Outcome Outbox, and remove the matching Proposal Attempt.
   */
  settleProposalAttempt(input: {
    attempt: ProposalAttempt;
    proposal: ActionProposal;
    report: ReportActionRequest;
    apply: () => void | Promise<void>;
  }): Promise<void>;
}

export interface WorkflowStore extends ProposalAttemptPersistence, OutcomeOutboxStore {
  settleProposalAttempt?(input: {
    attempt: ProposalAttempt;
    proposal: ActionProposal;
    report: ReportActionRequest;
    apply: () => void | Promise<void>;
  }): Promise<void>;
  completeProposalAttempt?(input: {
    attempt: ProposalAttempt;
    proposal: ActionProposal;
    report: ReportActionRequest;
  }): Promise<void>;
}

export interface OutcomeOutboxEntry {
  report: ReportActionRequest;
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
    report: ReportActionRequest,
    apply: () => void | Promise<void>,
  ): Promise<void>;
}

export class OutcomeOutbox {
  constructor(client: RinClient, store: OutcomeOutboxStore);
  drain(): Promise<number>;
}

export class WorkflowCoordinator {
  constructor(client: RinClient, store: WorkflowStore, durability?: HostDurability);
  readonly durability: HostDurability;
  begin(operationId: string, request: ProposeRequest): Promise<ProposalAttempt>;
  resumePendingWork(options?: RinPollingOptions): Promise<{
    attempt: ProposalAttempt;
    proposal: ActionProposal;
    duplicate: boolean;
  }>;
  applyAndEnqueueOutcome(input: {
    pendingTurn: ProposalAttempt;
    proposal: ActionProposal;
    report: ReportActionRequest;
    requiredDurability?: HostDurabilityProfile;
    apply(operationId: string): void | Promise<void>;
  }): Promise<void>;
  drainOutbox(): Promise<number>;
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
  observe(payload: ObserveRequest): Promise<MutationResult>;
  propose(payload: ProposeRequest): Promise<ProposalResult>;
  submitProposalJob(payload: RinObject): Promise<RinObject>;
  getProposalJob(jobId: string): Promise<RinObject>;
  cancelProposalJob(jobId: string): Promise<RinObject>;
  submitGenerationJob(payload: RinObject): Promise<RinObject>;
  getGenerationJob(jobId: string): Promise<RinObject>;
  cancelGenerationJob(jobId: string): Promise<RinObject>;
  reportAction(payload: ReportActionRequest): Promise<MutationResult>;
  reportActionBatch(payload: BatchActionReportRequest): Promise<MutationResult>;
  setActorActivity(payload: RinObject): Promise<RinObject>;
  arbitrate(payload: RinObject): Promise<RinObject>;
  state(payload: RinObject): Promise<RinObject>;
  sessionStats(payload: SessionRequest): Promise<SessionStats>;
  archiveSession(payload: ArchiveSessionRequest): Promise<ArchiveSessionResult>;
  deleteSession(payload: DeleteSessionRequest): Promise<DeleteSessionResult>;
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

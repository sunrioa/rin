export const SDK_VERSION: "0.7.0";
export const CONTROL_CONTRACT_VERSION: "rin.control/v2";
export const CONTROL_DEFAULT_BASE_URL: string;
export const CONTROL_MAX_RESPONSE_BYTES: number;

export type RinObject = Record<string, unknown>;
export type FetchImplementation = typeof globalThis.fetch;
export type ControlResponse = RinObject | unknown[];

export interface RinControlClientOptions {
  token: string;
  timeoutMs?: number;
  maxResponseBytes?: number;
  fetch?: FetchImplementation;
}

export class RinError extends Error { readonly code: string; }
export class RinConfigurationError extends RinError {}
export class RinTransportError extends RinError {}
export class RinProtocolError extends RinError {}
export class RinAPIError extends RinError {
  readonly status: number;
  readonly field: string;
}

export class RinControlClient {
  constructor(options: RinControlClientOptions);
  constructor(baseUrl: string | undefined, options: RinControlClientOptions);
  readonly baseUrl: string;
  info(): Promise<RinObject>;
  listWorlds(): Promise<ControlResponse>;
  listActors(input: RinObject): Promise<ControlResponse>;
  getActor(input: RinObject): Promise<ControlResponse>;
  waitActor(input: RinObject): Promise<ControlResponse>;
  observeActor(input: RinObject): Promise<ControlResponse>;
  listCapabilities(input: RinObject): Promise<ControlResponse>;
  describeCapability(input: RinObject): Promise<ControlResponse>;
  acquireController(input: RinObject): Promise<ControlResponse>;
  renewController(input: RinObject): Promise<ControlResponse>;
  releaseController(input: RinObject): Promise<ControlResponse>;
  getController(input: RinObject): Promise<ControlResponse>;
  submitAction(input: RinObject): Promise<ControlResponse>;
  confirmAction(input: RinObject): Promise<ControlResponse>;
  getOperation(input: RinObject): Promise<ControlResponse>;
  waitOperation(input: RinObject): Promise<ControlResponse>;
  cancelOperation(input: RinObject): Promise<ControlResponse>;
  setEmergencyStop(input: RinObject): Promise<ControlResponse>;
}

export type CanonicalJSONErrorCode =
  | "CANONICAL_JSON.INVALID_SYNTAX"
  | "CANONICAL_JSON.INVALID_UNICODE"
  | "CANONICAL_JSON.DUPLICATE_MEMBER"
  | "CANONICAL_JSON.UNSUPPORTED_NUMBER"
  | "CANONICAL_JSON.RESOURCE_LIMIT";

export interface CanonicalJSONLimits {
  maxInputBytes?: number;
  maxDepth?: number;
  maxValues?: number;
  maxStringBytes?: number;
  maxIntegerDigits?: number;
}

export declare const CANONICAL_JSON_PROFILE: "cutly.json.canonical-integer.v1";

export declare class CanonicalJSONError extends Error {
  readonly code: CanonicalJSONErrorCode;
  readonly offset: number;
  constructor(code: CanonicalJSONErrorCode, offset: number, detail: string);
}

export declare function canonicalizeJSONString(
  input: string | Uint8Array,
  limits?: CanonicalJSONLimits,
): string;

export declare function canonicalizeJSON(
  input: string | Uint8Array,
  limits?: CanonicalJSONLimits,
): Uint8Array;

export type CanonicalJSONValue =
  | null
  | boolean
  | string
  | bigint
  | readonly CanonicalJSONValue[]
  | { readonly [name: string]: CanonicalJSONValue };

export type CanonicalJSONInput =
  | null
  | boolean
  | string
  | number
  | bigint
  | readonly CanonicalJSONInput[]
  | { readonly [name: string]: CanonicalJSONInput };

export declare function encodeCanonicalValue(
  value: CanonicalJSONInput,
  limits?: CanonicalJSONLimits,
): Uint8Array;

export declare function decodeCanonicalJSON(
  input: string | Uint8Array,
  limits?: CanonicalJSONLimits,
): CanonicalJSONValue;

export interface CanonicalJSONDigest {
  profile: typeof CANONICAL_JSON_PROFILE;
  digest: `sha256:${string}`;
  canonical: Uint8Array;
}

export declare function digestCanonicalJSON(
  input: string | Uint8Array,
  limits?: CanonicalJSONLimits,
): Promise<CanonicalJSONDigest>;

export declare function digestCanonicalValue(
  value: CanonicalJSONInput,
  limits?: CanonicalJSONLimits,
): Promise<CanonicalJSONDigest>;

export type IntegerLike = string | number | bigint;

export interface RationalTimeLike {
  value: IntegerLike;
  rate: IntegerLike;
}

export interface FrameClockLike {
  origin: RationalTimeLike;
  framesPerSecond: RationalTimeLike;
}

export interface SampleClockLike {
  origin: RationalTimeLike;
  samplesPerSecond: IntegerLike;
}

export interface TickRangeLike {
  first: IntegerLike;
  count: IntegerLike;
}

export interface HeadlessPlayerConfig {
  revision: IntegerLike;
  frameClock: FrameClockLike;
  sampleClock: SampleClockLike;
  frameRange: TickRangeLike;
  sampleRange: TickRangeLike;
}

export type PlayerPhase = "idle" | "ready" | "playing" | "paused" | "buffering" | "ended";

export interface WireTickRange {
  first: string;
  count: string;
}

export interface PlayerSnapshot {
  profile: typeof PLAYER_PROFILE;
  revision: string;
  phase: PlayerPhase;
  desiredPlaying: boolean;
  frameIndex: string | null;
  sampleIndex: string | null;
  seekEpoch: string;
  bufferedFrameRanges: WireTickRange[];
  loopFrameRange: WireTickRange | null;
  presentedRequestID: string | null;
}

export interface FrameRequestToken {
  profile: typeof PLAYER_PROFILE;
  revision: string;
  seekEpoch: string;
  requestID: string;
  compositionFrameIndex: string;
}

export interface PlayerEvent {
  kind: "load" | "play" | "pause" | "seek" | "loop" | "loop-range" | "clock" | "ended" | "buffer" | "frame";
  state: PlayerSnapshot;
}

export declare const PLAYER_PROFILE: "cutly.player.exact-audio-master.v1";

export declare class PlayerStateError extends Error {
  readonly code: string;
}

export declare class HeadlessPlayer {
  constructor(config: HeadlessPlayerConfig);
  load(config: HeadlessPlayerConfig): PlayerSnapshot;
  snapshot(): PlayerSnapshot;
  subscribe(listener: (event: PlayerEvent) => void): () => boolean;
  play(): PlayerSnapshot;
  pause(): PlayerSnapshot;
  seekFrame(frameIndex: IntegerLike): PlayerSnapshot;
  advanceAudioSample(sampleIndex: IntegerLike): PlayerSnapshot;
  setBufferedFrameRanges(ranges: TickRangeLike[]): PlayerSnapshot;
  setLoopFrameRange(range: TickRangeLike | null): PlayerSnapshot;
  requestFrame(): FrameRequestToken;
  acceptFrame(token: FrameRequestToken): boolean;
}

export declare function frameIndexAtSample(
  frameClock: FrameClockLike,
  sampleClock: SampleClockLike,
  sampleIndex: IntegerLike,
): bigint;

export declare function sampleIndexAtFrame(
  frameClock: FrameClockLike,
  sampleClock: SampleClockLike,
  frameIndex: IntegerLike,
): bigint;

export interface ExactRationalTime {
  readonly value: bigint;
  readonly rate: bigint;
}

export interface TimeRangeInput {
  start: RationalTimeLike;
  duration: RationalTimeLike;
}

export type TypedOperation = Readonly<Record<string, CanonicalJSONValue>>;

export declare const OPERATION_PROTOCOL: "cutly.transaction.v1";
export declare const HEADLESS_TIMELINE_PROFILE: "cutly.headless-timeline.typed-operations.v1";
export declare const HEADLESS_UI_PROFILE: "cutly.headless-ui-primitives.v1";

export declare function insertClip(input: { id: string; trackId: string; clip: Record<string, CanonicalJSONInput> }): TypedOperation;
export declare function moveClip(input: { id: string; clipId: string; at: RationalTimeLike }): TypedOperation;
export declare function trimClip(input: { id: string; clipId: string; sourceRange: TimeRangeInput; timelineRange?: TimeRangeInput; ripple?: boolean }): TypedOperation;
export declare function splitClip(input: { id: string; clipId: string; newClipId: string; at: RationalTimeLike }): TypedOperation;
export declare function deleteClip(input: { id: string; clipId: string; ripple?: boolean }): TypedOperation;
export declare function retrackClip(input: { id: string; clipId: string; targetTrackId: string }): TypedOperation;
export declare function setClipProperty(input: { id: string; clipId: string; property: string; value?: CanonicalJSONInput }): TypedOperation;
export declare function buildTransaction(input: {
  id: string;
  idempotencyKey: string;
  expectedRevision: IntegerLike;
  operations: readonly TypedOperation[];
}): Readonly<Record<string, CanonicalJSONValue>>;

export interface TimelineSnapshot {
  profile: typeof HEADLESS_TIMELINE_PROFILE;
  revision: string;
  selectedTrackIDs: readonly string[];
  selectedClipIDs: readonly string[];
  playhead: { value: string; rate: string } | null;
}

export interface TimelineDispatch {
  profile: typeof HEADLESS_TIMELINE_PROFILE;
  expectedRevision: string;
  operation: TypedOperation;
}

export declare class HeadlessTimeline<DispatchResult = unknown> {
  constructor(input: { revision: IntegerLike; dispatch: (intent: TimelineDispatch) => DispatchResult });
  snapshot(): TimelineSnapshot;
  setRevision(revision: IntegerLike): TimelineSnapshot;
  select(input?: { trackIDs?: string[]; clipIDs?: string[] }): TimelineSnapshot;
  setPlayhead(value: RationalTimeLike): TimelineSnapshot;
  clearPlayhead(): TimelineSnapshot;
  insert(input: Parameters<typeof insertClip>[0]): DispatchResult;
  move(input: Parameters<typeof moveClip>[0]): DispatchResult;
  trim(input: Parameters<typeof trimClip>[0]): DispatchResult;
  split(input: Parameters<typeof splitClip>[0]): DispatchResult;
  delete(input: Parameters<typeof deleteClip>[0]): DispatchResult;
  retrack(input: Parameters<typeof retrackClip>[0]): DispatchResult;
  setProperty(input: Parameters<typeof setClipProperty>[0]): DispatchResult;
}

export declare function waveformLane(input: {
  id: string;
  trackId: string;
  assetId: string;
  sampleRange: TickRangeLike;
  peaksDigest: `sha256:${string}`;
}): Readonly<Record<string, CanonicalJSONValue>>;

export declare function captionLane(input: {
  id: string;
  trackId: string;
  cues: readonly { id: string; range: TimeRangeInput; text: string }[];
}): Readonly<Record<string, CanonicalJSONValue>>;

export interface UIClipPrimitive {
  readonly profile: typeof HEADLESS_UI_PROFILE;
  readonly kind: "clip";
  readonly id: string;
  readonly track_id: string;
  readonly asset_id: string;
  readonly timeline_range: { readonly start: ExactRationalTime; readonly duration: ExactRationalTime };
  readonly source_range: { readonly start: ExactRationalTime; readonly duration: ExactRationalTime };
  readonly disabled: boolean;
  readonly label?: string;
}

export interface UITrackPrimitive {
  readonly profile: typeof HEADLESS_UI_PROFILE;
  readonly kind: "track";
  readonly id: string;
  readonly track_kind: "video" | "audio";
  readonly clips: readonly UIClipPrimitive[];
  readonly label?: string;
}

export interface UITimelinePrimitive {
  readonly profile: typeof HEADLESS_UI_PROFILE;
  readonly kind: "timeline";
  readonly id: string;
  readonly revision: bigint;
  readonly tracks: readonly UITrackPrimitive[];
  readonly label?: string;
}

export interface UITrimHandlePrimitive {
  readonly profile: typeof HEADLESS_UI_PROFILE;
  readonly kind: "trim_handle";
  readonly clip_id: string;
  readonly track_id: string;
  readonly edge: "start" | "end";
  readonly timeline_time: ExactRationalTime;
  readonly source_time: ExactRationalTime;
}

export interface UIPlayheadPrimitive {
  readonly profile: typeof HEADLESS_UI_PROFILE;
  readonly kind: "playhead";
  readonly time: ExactRationalTime;
  readonly frame_index?: bigint;
  readonly sample_index?: bigint;
}

export interface UISelectionPrimitive {
  readonly profile: typeof HEADLESS_UI_PROFILE;
  readonly kind: "selection";
  readonly track_ids: readonly string[];
  readonly clip_ids: readonly string[];
}

export declare function clipPrimitive(input: {
  id: string;
  trackId: string;
  assetId: string;
  timelineRange: TimeRangeInput;
  sourceRange: TimeRangeInput;
  disabled?: boolean;
  label?: string;
}): UIClipPrimitive;

export declare function trackPrimitive(input: {
  id: string;
  kind: "video" | "audio";
  clips: readonly UIClipPrimitive[];
  label?: string;
}): UITrackPrimitive;

export declare function timelinePrimitive(input: {
  id: string;
  revision: IntegerLike;
  tracks: readonly UITrackPrimitive[];
  label?: string;
}): UITimelinePrimitive;

export declare function trimHandle(input: {
  clip: UIClipPrimitive;
  edge: "start" | "end";
}): UITrimHandlePrimitive;

export declare function playheadPrimitive(input: {
  time: RationalTimeLike;
  frameIndex?: IntegerLike;
  sampleIndex?: IntegerLike;
}): UIPlayheadPrimitive;

export declare function selectionPrimitive(input?: {
  trackIDs?: readonly string[];
  clipIDs?: readonly string[];
}): UISelectionPrimitive;

export type AgentTool = "cutly_project" | "cutly_edit" | "cutly_render";
export type ProjectAction = "inspect" | "status" | "history" | "capabilities";
export type EditAction = "plan" | "apply" | "discard" | "revert";
export type RenderAction = "preflight" | "frame" | "render" | "status" | "cancel";
export type AgentAction = ProjectAction | EditAction | RenderAction;

export interface AgentRequestInput {
  requestId: string;
  tool: AgentTool;
  action: AgentAction;
  projectId?: string;
  payload?: Readonly<Record<string, CanonicalJSONInput>>;
}

export interface AgentRequest {
  readonly protocol_version: 1n;
  readonly request_id: string;
  readonly tool: AgentTool;
  readonly action: AgentAction;
  readonly project_id?: string;
  readonly payload: Readonly<Record<string, CanonicalJSONValue>>;
}

export interface AgentCandidateFix {
  readonly description: string;
  readonly operation?: Readonly<Record<string, CanonicalJSONValue>>;
  readonly side_effects?: readonly string[];
}

export interface AgentDiagnostic {
  readonly code: string;
  readonly severity: "info" | "warning" | "error";
  readonly path?: string;
  readonly message: string;
  readonly fixes?: readonly AgentCandidateFix[];
}

export interface AgentNextAction {
  readonly tool: AgentTool;
  readonly action: AgentAction;
  readonly project_id?: string;
  readonly revision?: bigint;
  readonly job_id?: string;
}

export interface AgentResult {
  readonly protocol_version: 1n;
  readonly ok: boolean;
  readonly request_id: string;
  readonly tool: AgentTool;
  readonly action: AgentAction;
  readonly state: string;
  readonly project_id?: string;
  readonly transaction_id?: string;
  readonly job_id?: string;
  readonly base_revision?: bigint;
  readonly new_revision?: bigint;
  readonly changed: boolean;
  readonly replayed: boolean;
  readonly render_required: boolean;
  readonly diagnostics: readonly AgentDiagnostic[];
  readonly diff?: Readonly<Record<string, CanonicalJSONValue>>;
  readonly receipt?: Readonly<Record<string, CanonicalJSONValue>>;
  readonly next_actions: readonly AgentNextAction[];
}

export type AgentProtocolErrorCode =
  | "AGENT.REQUEST_INVALID"
  | "AGENT.RESULT_INVALID"
  | "AGENT.TRANSPORT"
  | "AGENT.CORRELATION"
  | "AGENT.TOOL_ACTION_INVALID"
  | "AGENT.IDENTITY_INVALID"
  | "AGENT.OBJECT_REQUIRED"
  | "AGENT.ENDPOINT_INVALID"
  | "AGENT.HTTP_STATUS"
  | "AGENT.CONTENT_TYPE"
  | "AGENT.RESPONSE_LIMIT";

export declare const AGENT_PROTOCOL_VERSION: 1n;
export declare const AGENT_MEDIA_TYPE: "application/cutly-agent+json";
export declare const MAX_AGENT_REQUEST_BYTES: 4194304;
export declare const MAX_AGENT_RESULT_BYTES: 16777216;

export declare class AgentProtocolError extends Error {
  readonly code: AgentProtocolErrorCode;
  constructor(code: AgentProtocolErrorCode, detail: string, cause?: unknown);
}

export declare function createAgentRequest(input: AgentRequestInput): AgentRequest;

export type AgentTransportFunction = (
  canonicalRequest: Uint8Array,
  options: { signal?: AbortSignal },
) => Promise<string | Uint8Array> | string | Uint8Array;

export interface AgentTransport {
  roundTrip(
    canonicalRequest: Uint8Array,
    options?: { signal?: AbortSignal },
  ): Promise<string | Uint8Array> | string | Uint8Array;
}

export interface AgentActionIdentity {
  requestId: string;
  projectId: string;
}

export interface AgentTransactionAction extends AgentActionIdentity {
  transactionId: string;
}

export interface AgentRevisionAction extends AgentTransactionAction {
  expectedRevision: IntegerLike;
}

export interface AgentJobAction extends AgentActionIdentity {
  jobId: string;
}

export declare class CutlyClient {
  constructor(transport: AgentTransport | AgentTransportFunction);
  call(request: AgentRequest, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  callAction(input: AgentRequestInput, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  inspectProject(input: AgentActionIdentity, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  projectStatus(input: AgentActionIdentity, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  projectHistory(input: AgentActionIdentity & {
    afterSequence?: IntegerLike;
    limit?: IntegerLike;
  }, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  planEdit(input: AgentActionIdentity & {
    transaction: Readonly<Record<string, CanonicalJSONInput>>;
  }, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  applyEdit(input: AgentRevisionAction, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  discardEdit(input: AgentTransactionAction, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  revertEdit(input: AgentRevisionAction, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  submitRender(input: AgentActionIdentity & {
    job: Readonly<Record<string, CanonicalJSONInput>>;
  }, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  renderStatus(input: AgentJobAction, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  cancelRender(input: AgentJobAction, options?: { signal?: AbortSignal }): Promise<AgentResult>;
}

export interface HTTPAgentTransportOptions {
  fetch?: typeof globalThis.fetch;
  headers?: HeadersInit;
  maxResponseBytes?: number;
}

export declare class HTTPAgentTransport implements AgentTransport {
  readonly endpoint: string;
  readonly maxResponseBytes: number;
  constructor(endpoint: string, options?: HTTPAgentTransportOptions);
  roundTrip(canonicalRequest: Uint8Array, options?: { signal?: AbortSignal }): Promise<Uint8Array>;
}

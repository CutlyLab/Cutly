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
export declare const NUMBER_PRESERVING_JSON_PROFILE: "cutly.json.number-preserving.v1";

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

export declare function canonicalizeNumberPreservingJSONString(
  input: string | Uint8Array,
  limits?: CanonicalJSONLimits,
): string;

export declare function canonicalizeNumberPreservingJSON(
  input: string | Uint8Array,
  limits?: CanonicalJSONLimits,
): Uint8Array;

export declare function extractNumberPreservingRootMember(
  input: string | Uint8Array,
  member: string,
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

export interface NumberPreservingJSONDigest {
  profile: typeof NUMBER_PRESERVING_JSON_PROFILE;
  digest: `sha256:${string}`;
  canonical: Uint8Array;
}

export declare function digestNumberPreservingJSON(
  input: string | Uint8Array,
  limits?: CanonicalJSONLimits,
): Promise<NumberPreservingJSONDigest>;

export declare function digestNumberPreservingJSONWithoutRootMember(
  input: string | Uint8Array,
  member: string,
  limits?: CanonicalJSONLimits,
): Promise<NumberPreservingJSONDigest>;

export declare const MOTION_COMPOSITION_V2_PROFILE: "cutly.motion-composition.document-v2.v1";

export declare class MotionCompositionV2Error extends Error {
  readonly code: string;
  constructor(code: string, detail: string);
}

export type MotionIntegerV2 = string | number | bigint;
export type MotionExactV2 = MotionIntegerV2 | Readonly<{ numerator: MotionIntegerV2; denominator: MotionIntegerV2 }>;
export interface MotionRationalV2 { readonly numerator: bigint; readonly denominator: bigint; }
export interface MotionPointV2 { readonly x: MotionRationalV2; readonly y: MotionRationalV2; }
export interface MotionColorV2 { r?: MotionIntegerV2; g?: MotionIntegerV2; b?: MotionIntegerV2; a?: MotionIntegerV2; }
export interface MotionCubicBezierV2 { readonly x1: MotionRationalV2; readonly y1: MotionRationalV2; readonly x2: MotionRationalV2; readonly y2: MotionRationalV2; }

export declare function motionRationalV2(value: MotionExactV2, denominator?: MotionIntegerV2): MotionRationalV2;
export declare function motionPointV2(x: MotionExactV2, y: MotionExactV2): MotionPointV2;
export declare function motionEaseV2(name: "expo.out" | "power3.out" | "power2.inOut"): MotionCubicBezierV2;

export interface MotionCompositionV2Config {
  id: string;
  timelineId?: string;
  name?: string;
  width: MotionIntegerV2;
  height: MotionIntegerV2;
  fps: MotionIntegerV2;
  durationFrames: MotionIntegerV2;
  revision?: MotionIntegerV2;
}

export interface MotionResourceAssetV2 {
  id: string;
  uri: string;
  contentHash: `sha256:${string}`;
}

export interface MotionTextAssetV2 {
  id: string;
  text: string;
  fontAssetId: string;
  fontSize: MotionExactV2;
  lineHeight: MotionExactV2;
  width: MotionIntegerV2;
  height: MotionIntegerV2;
  fill: MotionColorV2;
  language?: string;
  script?: string;
  direction?: "ltr" | "rtl";
  alignment?: "start" | "center" | "end";
}

export interface MotionRectangleAssetV2 {
  id: string;
  width: MotionIntegerV2;
  height: MotionIntegerV2;
  fill: MotionColorV2;
}

export interface MotionAudioBusV2 {
  id: string;
  kind: "group" | "master";
  name?: string;
  parentBusId?: string;
  gain?: MotionExactV2;
  pan?: MotionExactV2;
  limiter?: MotionExactV2;
}

export interface MotionLayerOptionsV2 {
  name?: string;
  startFrame?: MotionIntegerV2;
  durationFrames?: MotionIntegerV2;
  sourceStartFrame?: MotionIntegerV2;
  audioBusId?: string;
  disabled?: boolean;
}

export interface MotionLayerSpecV2 extends MotionLayerOptionsV2 {
  id: string;
  trackKind: "video" | "audio" | "text" | "vector";
  assetId: string;
  durationFrames: MotionIntegerV2;
}

export interface MotionPointAnimationKeyframeV2 {
  frame: MotionIntegerV2;
  value: readonly [MotionExactV2, MotionExactV2];
  interpolation?: "linear" | "hold";
  easing?: "linear" | "expo.out" | "power3.out" | "power2.inOut" | Readonly<{ x1: MotionExactV2; y1: MotionExactV2; x2: MotionExactV2; y2: MotionExactV2 }>;
}

export interface MotionScalarAnimationKeyframeV2 {
  frame: MotionIntegerV2;
  value: MotionExactV2;
  interpolation?: "linear" | "hold";
  easing?: MotionPointAnimationKeyframeV2["easing"];
}

export declare class MotionLayerV2 {
  position(x: MotionExactV2, y: MotionExactV2): this;
  scale(x: MotionExactV2, y?: MotionExactV2): this;
  rotate(degrees: MotionExactV2): this;
  anchor(x: MotionExactV2, y: MotionExactV2): this;
  opacity(value: MotionExactV2): this;
  crop(value: { x: MotionExactV2; y: MotionExactV2; width: MotionExactV2; height: MotionExactV2 }): this;
  animatePosition(frames: readonly MotionPointAnimationKeyframeV2[]): this;
  animateScale(frames: readonly MotionPointAnimationKeyframeV2[]): this;
  animateRotation(frames: readonly MotionScalarAnimationKeyframeV2[]): this;
  animateOpacity(frames: readonly MotionScalarAnimationKeyframeV2[]): this;
}

export declare class MotionSceneV2 {
  image(id: string, assetId: string, options?: MotionLayerOptionsV2): MotionLayerV2;
  text(id: string, assetId: string, options?: MotionLayerOptionsV2): MotionLayerV2;
  shape(id: string, assetId: string, options?: MotionLayerOptionsV2): MotionLayerV2;
  audio(id: string, assetId: string, options?: MotionLayerOptionsV2): MotionLayerV2;
}

export declare class MotionCompositionV2 {
  constructor(config: MotionCompositionV2Config);
  imageAsset(spec: MotionResourceAssetV2): this;
  fontAsset(spec: MotionResourceAssetV2): this;
  audioAsset(spec: MotionResourceAssetV2 & { durationFrames: MotionIntegerV2 }): this;
  textAsset(spec: MotionTextAssetV2): this;
  rectangleAsset(spec: MotionRectangleAssetV2): this;
  audioBus(spec: MotionAudioBusV2): this;
  scene(id: string, range: { startFrame?: MotionIntegerV2; durationFrames: MotionIntegerV2 }, author?: (scene: MotionSceneV2) => void): MotionSceneV2;
  layer(spec: MotionLayerSpecV2): MotionLayerV2;
  dissolve(spec: { id: string; outgoingLayerId: string; incomingLayerId: string; preFrames?: MotionIntegerV2; postFrames?: MotionIntegerV2 }): this;
  build(): Readonly<Record<string, unknown>>;
  readonly durationFrames: bigint;
  readonly fps: bigint;
}

export declare function createMotionCompositionV2(config: MotionCompositionV2Config): MotionCompositionV2;

export declare const TRANSACTION_V2_PROFILE: "cutly.transaction.document-v2.number-preserving.v2";
export declare const TRANSACTION_V2_SCHEMA_VERSION: 2;
export declare const MAX_TRANSACTION_V2_BYTES: number;

export declare class TransactionV2Error extends Error {
  readonly code: string;
  constructor(code: string, detail: string);
}

export interface DecodedTransactionV2 {
  readonly schemaVersion: 2;
  readonly profile: typeof TRANSACTION_V2_PROFILE;
  readonly id: string;
  readonly idempotencyKey: string;
  readonly documentId: string;
  readonly expectedRevision: string;
  readonly expectedDocumentHash: `sha256:${string}`;
  readonly digest: `sha256:${string}`;
  readonly operationCount: number;
  readonly canonical: Uint8Array;
}

export declare function decodeTransactionV2(input: string | Uint8Array): Promise<DecodedTransactionV2>;
export declare function transactionDigestV2(input: string | Uint8Array): Promise<`sha256:${string}`>;
export declare function moveClipV2(input: { id: string; clipId: string; at: RationalTimeLike }): TypedOperation;
export declare function trimClipV2(input: { id: string; clipId: string; timelineRange: TimeRangeInput; sourceRange: TimeRangeInput }): TypedOperation;
export declare function deleteClipV2(input: { id: string; trackId: string; clipId: string }): TypedOperation;
export declare function retrackClipV2(input: { id: string; clipId: string; trackId: string; position: IntegerLike }): TypedOperation;
export declare function setClipPropertiesV2(input: { id: string; clipId: string; value?: CanonicalJSONInput }): TypedOperation;
export declare function buildTransactionV2(input: {
  id: string;
  idempotencyKey: string;
  documentId: string;
  expectedRevision: IntegerLike;
  expectedDocumentHash: `sha256:${string}`;
  operations: readonly TypedOperation[];
}): Promise<DecodedTransactionV2>;

export declare const DOCUMENT_V2_UI_PROFILE: "cutly.headless-ui.document-v2.v2";
export interface DocumentV2UIClipPrimitive {
  readonly profile: typeof DOCUMENT_V2_UI_PROFILE;
  readonly kind: "clip";
  readonly id: string;
  readonly track_id: string;
  readonly track_kind: "video" | "audio" | "text" | "vector" | "annotation";
  readonly asset_id: string;
  readonly asset_kind: string;
  readonly timeline_range: { readonly start: ExactRationalTime; readonly duration: ExactRationalTime };
  readonly source_range: { readonly start: ExactRationalTime; readonly duration: ExactRationalTime };
  readonly disabled: boolean;
  readonly keyframe_lane_ids: readonly string[];
}
export interface DocumentV2UITrackPrimitive {
  readonly profile: typeof DOCUMENT_V2_UI_PROFILE;
  readonly kind: "track";
  readonly id: string;
  readonly track_kind: "video" | "audio" | "text" | "vector" | "annotation";
  readonly audio_bus_id: string;
  readonly clips: readonly DocumentV2UIClipPrimitive[];
}
export interface DocumentV2UITimelinePrimitive {
  readonly profile: typeof DOCUMENT_V2_UI_PROFILE;
  readonly kind: "timeline";
  readonly document_id: string;
  readonly timeline_id: string;
  readonly revision: bigint;
  readonly tracks: readonly DocumentV2UITrackPrimitive[];
}
export declare function documentV2TimelinePrimitives(document: Readonly<Record<string, unknown>>, timelineId: string): DocumentV2UITimelinePrimitive;

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
  profile: typeof PLAYER_PROFILE | typeof DOCUMENT_V2_PLAYER_PROFILE;
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
  profile: typeof PLAYER_PROFILE | typeof DOCUMENT_V2_PLAYER_PROFILE;
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
export declare const DOCUMENT_V2_PLAYER_PROFILE: "cutly.player.document-v2.exact-audio-master.v2";

export type PlayerStateErrorCode =
  | "PLAYER.BUFFER_RANGE_INVALID"
  | "PLAYER.CLOCK_INVALID"
  | "PLAYER.INTEGER_INVALID"
  | "PLAYER.LOOP_RANGE_INVALID"
  | "PLAYER.NO_FRAME"
  | "PLAYER.PROFILE_INVALID"
  | "PLAYER.RANGE_INVALID"
  | "PLAYER.TIME_OVERFLOW";

export declare class PlayerStateError extends Error {
  readonly code: PlayerStateErrorCode;
  constructor(code: PlayerStateErrorCode, detail: string);
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

export declare class DocumentV2Player extends HeadlessPlayer {
  constructor(config: HeadlessPlayerConfig);
  load(config: HeadlessPlayerConfig): PlayerSnapshot;
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

export type AgentToolV2 = "cutly_project" | "cutly_edit" | "cutly_render";
export type AgentActionV2 = ProjectAction | EditAction | "plan" | "prepare" | "preflight" | "frame" | "submit" | "status" | "history" | "cancel";
export declare const AGENT_V2_SCHEMA_VERSION: 2;
export declare const AGENT_V2_REQUEST_PROFILE: "cutly.agent-request.document-v2.v2";
export declare const AGENT_V2_RESULT_PROFILE: "cutly.agent-result.document-v2.v2";
export declare const AGENT_V2_MEDIA_TYPE: "application/cutly-agent-document-v2+json";
export declare class AgentProtocolV2Error extends Error {
  readonly code: string;
  constructor(code: string, detail: string, cause?: unknown);
}
export interface AgentRequestV2Handle {
  readonly requestId: string;
  readonly tool: AgentToolV2;
  readonly action: AgentActionV2;
  readonly projectId: string;
  readonly canonical: Uint8Array;
}
export interface AgentResultV2Handle {
  readonly schemaVersion: 2;
  readonly profile: typeof AGENT_V2_RESULT_PROFILE;
  readonly ok: boolean;
  readonly requestId: string;
  readonly tool: AgentToolV2;
  readonly action: AgentActionV2;
  readonly projectId: string;
  readonly state: string;
  readonly diagnostics: readonly AgentDiagnostic[];
  readonly receipt: Uint8Array;
  readonly canonical: Uint8Array;
}
export declare function createAgentRequestV2(input: {
  requestId: string;
  tool: AgentToolV2;
  action: AgentActionV2;
  projectId: string;
  payload?: CanonicalJSONInput | string | Uint8Array;
}): AgentRequestV2Handle;
export declare function createAgentTransactionRequestV2(input: {
  requestId: string;
  projectId: string;
  transaction: DecodedTransactionV2;
}): AgentRequestV2Handle;
export declare function decodeAgentResultV2(input: string | Uint8Array): AgentResultV2Handle;
export interface AgentV2Transport {
  roundTrip(request: Uint8Array, options?: { signal?: AbortSignal }): Promise<string | Uint8Array> | string | Uint8Array;
}
export declare class CutlyAgentV2Client {
  constructor(transport: AgentV2Transport | ((request: Uint8Array, options?: { signal?: AbortSignal }) => Promise<string | Uint8Array> | string | Uint8Array));
  call(request: AgentRequestV2Handle, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  callAction(input: Parameters<typeof createAgentRequestV2>[0], options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  inspectProject(input: AgentActionIdentity, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  projectStatus(input: AgentActionIdentity, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  projectHistory(input: AgentActionIdentity & { afterSequence?: IntegerLike; limit?: IntegerLike }, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  planEdit(input: AgentActionIdentity & { transaction: DecodedTransactionV2 }, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  applyEdit(input: AgentTransactionAction, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  discardEdit(input: AgentTransactionAction, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  revertEdit(input: AgentTransactionAction & { targetTransactionId: string; idempotencyKey: string }, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  capabilities(input: AgentActionIdentity, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  planRender(input: AgentActionIdentity & { range: CanonicalJSONInput; target: CanonicalJSONInput }, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  prepareRender(input: AgentActionIdentity & { environment: CanonicalJSONInput | string | Uint8Array }, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  preflightRender(input: AgentActionIdentity & { planFingerprint: string; executionManifestFingerprint: string }, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  renderFrame(input: AgentActionIdentity & { planFingerprint: string; executionManifestFingerprint: string; compositionFrameIndex: IntegerLike }, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  submitRender(input: AgentActionIdentity & { job: CanonicalJSONInput }, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  renderStatus(input: AgentJobAction, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  renderHistory(input: AgentJobAction & { afterSequence?: IntegerLike; limit?: IntegerLike }, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
  cancelRender(input: AgentJobAction, options?: { signal?: AbortSignal }): Promise<AgentResultV2Handle>;
}
export declare class HTTPAgentV2Transport implements AgentV2Transport {
  constructor(endpoint: string | URL, options?: { fetch?: typeof fetch });
  roundTrip(request: Uint8Array, options?: { signal?: AbortSignal }): Promise<Uint8Array>;
}

export declare const DOCUMENT_V2_FRAME_PREVIEW_PROFILE: "cutly.preview-frame.document-v2.v2";
export declare const DOCUMENT_V2_AUDIO_PREVIEW_PROFILE: "cutly.preview-audio.document-v2.v2";
export declare class DocumentV2PreviewError extends Error {
  readonly code: string;
  constructor(code: string, detail: string, cause?: unknown);
}
export interface DocumentV2FramePreviewRequest {
  readonly profile: typeof DOCUMENT_V2_FRAME_PREVIEW_PROFILE;
  readonly plan_fingerprint: `sha256:${string}`;
  readonly execution_manifest_fingerprint: `sha256:${string}`;
  readonly composition_frame_index: string;
}
export interface DocumentV2FramePreviewResult extends DocumentV2FramePreviewRequest {
  readonly content_hash: `sha256:${string}`;
  readonly uri: string;
}
export interface DocumentV2AudioPreviewRequest {
  readonly profile: typeof DOCUMENT_V2_AUDIO_PREVIEW_PROFILE;
  readonly plan_fingerprint: `sha256:${string}`;
  readonly execution_manifest_fingerprint: `sha256:${string}`;
  readonly sample_range: { readonly first: string; readonly count: string };
}
export interface DocumentV2AudioPreviewResult extends DocumentV2AudioPreviewRequest {
  readonly content_hash: `sha256:${string}`;
  readonly pcm_profile: string;
}
export declare class DocumentV2PreviewAdapter {
  constructor(config: {
    planFingerprint: `sha256:${string}`;
    executionManifestFingerprint: `sha256:${string}`;
    renderFrame: (request: DocumentV2FramePreviewRequest, options: { signal?: AbortSignal }) => Promise<DocumentV2FramePreviewResult> | DocumentV2FramePreviewResult;
    renderAudio?: (request: DocumentV2AudioPreviewRequest, options: { signal?: AbortSignal }) => Promise<DocumentV2AudioPreviewResult> | DocumentV2AudioPreviewResult;
  });
  renderCurrentFrame(player: DocumentV2Player, options?: { signal?: AbortSignal }): Promise<Readonly<{ request: DocumentV2FramePreviewRequest; frame: DocumentV2FramePreviewResult; token: Readonly<Record<string, string>>; presented: boolean }>>;
  renderAudioRange(range: { first: IntegerLike; count: IntegerLike }, options?: { signal?: AbortSignal }): Promise<DocumentV2AudioPreviewResult>;
}

export type AgentTool = "cutly_project" | "cutly_edit" | "cutly_render";
export type ProjectAction = "inspect" | "status" | "history" | "capabilities";
export type EditAction = "plan" | "apply" | "discard" | "revert";
export type RenderAction = "plan" | "prepare" | "preflight" | "frame" | "render" | "status" | "history" | "cancel";
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

export type WireRationalTimeV1 = Readonly<Record<string, CanonicalJSONValue>> & {
  readonly value: bigint;
  readonly rate: bigint;
}

export type WireTimeRangeV1 = Readonly<Record<string, CanonicalJSONValue>> & {
  readonly start: WireRationalTimeV1;
  readonly duration: WireRationalTimeV1;
}

export type WireTickRangeV1 = Readonly<Record<string, CanonicalJSONValue>> & {
  readonly first: bigint;
  readonly count: bigint;
}

export type RenderTargetV3Value = Readonly<Record<string, CanonicalJSONValue>> & {
  readonly timeline_id: string;
  readonly width: bigint;
  readonly height: bigint;
  readonly frame_clock: Readonly<{
    origin: WireRationalTimeV1;
    frames_per_second: WireRationalTimeV1;
  }>;
  readonly sample_clock: Readonly<{
    origin: WireRationalTimeV1;
    samples_per_second: bigint;
  }>;
  readonly channels: bigint;
  readonly pixel_format: string;
  readonly color_space: string;
}

export type RenderPlanResourceV3Value = Readonly<Record<string, CanonicalJSONValue>> & {
  readonly asset_id: string;
  readonly kind: "video" | "audio" | "image" | "font" | "data";
  readonly content_hash?: string;
}

export type RenderPlanV3Value = Readonly<Record<string, CanonicalJSONValue>> & {
  readonly schema_version: 3n;
  readonly semantic_profile: "cutly.semantic.v1";
  readonly document_id: string;
  readonly revision: bigint;
  readonly document_hash: string;
  readonly timeline_id: string;
  readonly requested_range: WireTimeRangeV1;
  readonly target: RenderTargetV3Value;
  readonly frame_range: WireTickRangeV1;
  readonly sample_range: WireTickRangeV1;
  readonly required_capabilities: Readonly<Record<string, CanonicalJSONValue>>;
  readonly video?: readonly Readonly<Record<string, CanonicalJSONValue>>[];
  readonly audio?: readonly Readonly<Record<string, CanonicalJSONValue>>[];
  readonly resources?: readonly RenderPlanResourceV3Value[];
  readonly fingerprint: string;
}

export interface RenderPlanReceiptV1 {
  readonly profile: "cutly.agent-render-plan.v1";
  readonly plan: RenderPlanV3Value;
}

export type MaterializedResourceV1Value = Readonly<Record<string, CanonicalJSONValue>> & {
  readonly asset_id: string;
  readonly kind: "video" | "audio" | "image" | "font" | "data";
  readonly content_hash: string;
  readonly size_bytes: bigint;
  readonly locator_scheme: "cas" | "file";
  readonly locator: string;
  readonly probe_fingerprint: string;
}

export type ExecutionManifestV1Value = Readonly<Record<string, CanonicalJSONValue>> & {
  readonly schema_version: 1n;
  readonly plan_fingerprint: string;
  readonly semantic_profile: "cutly.semantic.v1";
  readonly compiler_build: string;
  readonly evaluator_build: string;
  readonly resources?: readonly MaterializedResourceV1Value[];
  readonly fonts?: readonly Readonly<Record<string, CanonicalJSONValue>>[];
  readonly plugins?: readonly Readonly<Record<string, CanonicalJSONValue>>[];
  readonly backend: Readonly<Record<string, CanonicalJSONValue>> & {
    readonly backend_id: string;
    readonly build_fingerprint: string;
  };
  readonly media_runtime: Readonly<Record<string, CanonicalJSONValue>>;
  readonly output: Readonly<Record<string, CanonicalJSONValue>>;
  readonly deterministic_seed: bigint;
  readonly browser_fingerprint?: string;
  readonly fingerprint: string;
}

export interface RenderPrepareReceiptV1 {
  readonly profile: "cutly.agent-render-prepare.v1";
  readonly execution_manifest: ExecutionManifestV1Value;
}

export interface RenderPreflightReceiptV1 {
  readonly profile: "cutly.agent-render-preflight.v1";
  readonly plan_fingerprint: string;
  readonly execution_manifest_fingerprint: string;
  readonly backend_id: string;
  readonly backend_build_fingerprint: string;
}

export type AgentReceiptErrorCode =
  | "RECEIPT.INPUT_INVALID"
  | "RECEIPT.RESULT_INVALID"
  | "RECEIPT.CORRELATION_INVALID"
  | "RECEIPT.RENDER_REJECTED"
  | "RECEIPT.PROFILE_INVALID"
  | "RECEIPT.INVALID"
  | "RECEIPT.BINDING_INVALID"
  | "RECEIPT.FINGERPRINT_INVALID";

export declare class AgentReceiptError extends Error {
  readonly code: AgentReceiptErrorCode;
  constructor(code: AgentReceiptErrorCode, detail: string, cause?: unknown);
}

export interface RenderPlanReceiptBinding {
  requestId: string;
  projectId: string;
  range: Readonly<Record<string, CanonicalJSONInput>>;
  target: Readonly<Record<string, CanonicalJSONInput>>;
}

export interface RenderPrepareReceiptBinding {
  requestId: string;
  projectId: string;
  plan: Readonly<Record<string, CanonicalJSONInput>>;
  resources: readonly RenderPrepareResource[];
  deterministicSeed?: number | bigint;
}

export interface RenderPreflightReceiptBinding {
  requestId: string;
  projectId: string;
  plan: Readonly<Record<string, CanonicalJSONInput>>;
  executionManifest: Readonly<Record<string, CanonicalJSONInput>>;
}

export declare function decodeRenderPlanReceipt(
  result: AgentResult,
  expected: RenderPlanReceiptBinding,
): Promise<RenderPlanReceiptV1>;

export declare function decodeRenderPrepareReceipt(
  result: AgentResult,
  expected: RenderPrepareReceiptBinding,
): Promise<RenderPrepareReceiptV1>;

export declare function decodeRenderPreflightReceipt(
  result: AgentResult,
  expected: RenderPreflightReceiptBinding,
): Promise<RenderPreflightReceiptV1>;

export interface RenderPrepareResource {
  assetId: string;
  locator: string;
}

export interface RenderJobV1 {
  readonly schema_version: 1n;
  readonly id: string;
  readonly idempotency_key: string;
  readonly plan_fingerprint: string;
  readonly execution_manifest_fingerprint: string;
  readonly timeline_range: WireTimeRangeV1;
  readonly frame_range: WireTickRangeV1;
  readonly sample_range: WireTickRangeV1;
  readonly artifact_uri: string;
}

export type RenderJobState = "queued" | "running" | "canceling" | "canceled" | "failed" | "complete";

export interface RenderArtifactV1 {
  readonly uri: string;
  readonly content_hash: string;
  readonly size_bytes: bigint;
  readonly execution_manifest_fingerprint: string;
  readonly evidence_uri: string;
  readonly evidence_fingerprint: string;
  readonly duration: WireRationalTimeV1;
  readonly frame_count: bigint;
  readonly sample_count: bigint;
}

export interface RenderJobSnapshotV1 {
  readonly schema_version: 1n;
  readonly job: RenderJobV1;
  readonly state: RenderJobState;
  readonly last_sequence: bigint;
  readonly failure_class?: string;
  readonly failure_message?: string;
  readonly artifact?: RenderArtifactV1;
}

export type RenderJobEventKind = "submitted" | "started" | "cancel_requested" | "canceled" | "failed" | "completed";

export interface RenderJobEventV1 {
  readonly schema_version: 1n;
  readonly job_id: string;
  readonly sequence: bigint;
  readonly kind: RenderJobEventKind;
  readonly state: RenderJobState;
  readonly job?: RenderJobV1;
  readonly failure_class?: string;
  readonly failure_message?: string;
  readonly artifact?: RenderArtifactV1;
}

export interface RenderJobHistoryPageV1 {
  readonly schema_version: 1n;
  readonly profile: "cutly.render-job-history.v1";
  readonly after_sequence: bigint;
  readonly next_after_sequence: bigint;
  readonly caught_up: boolean;
  readonly events: readonly RenderJobEventV1[];
  readonly snapshot: RenderJobSnapshotV1;
}

export interface RenderJobSnapshotReceiptBinding {
  requestId: string;
  projectId: string;
  action: "render" | "status" | "cancel";
  job: RenderJobV1;
}

export declare function decodeRenderJobSnapshotReceipt(
  result: AgentResult,
  expected: RenderJobSnapshotReceiptBinding,
): RenderJobSnapshotV1;

export interface RenderJobHistoryPageReceiptBinding {
  requestId: string;
  projectId: string;
  job: RenderJobV1;
  afterSequence: number | bigint;
}

export declare function decodeRenderJobHistoryPageReceipt(
  result: AgentResult,
  expected: RenderJobHistoryPageReceiptBinding,
): RenderJobHistoryPageV1;

export declare function createRenderJob(input: {
  jobId: string;
  idempotencyKey: string;
  artifactUri: string;
  plan: Readonly<Record<string, CanonicalJSONInput>> & {
    readonly fingerprint: string;
    readonly requested_range: Readonly<Record<string, CanonicalJSONInput>>;
    readonly frame_range: Readonly<Record<string, CanonicalJSONInput>>;
    readonly sample_range: Readonly<Record<string, CanonicalJSONInput>>;
  };
  executionManifest: Readonly<Record<string, CanonicalJSONInput>> & {
    readonly fingerprint: string;
    readonly plan_fingerprint: string;
  };
}): RenderJobV1;

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
  planRender(input: AgentActionIdentity & {
    range: Readonly<Record<string, CanonicalJSONInput>>;
    target: Readonly<Record<string, CanonicalJSONInput>>;
  }, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  prepareRender(input: AgentActionIdentity & {
    plan: Readonly<Record<string, CanonicalJSONInput>>;
    resources: readonly RenderPrepareResource[];
    deterministicSeed?: number | bigint;
  }, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  preflightRender(input: AgentActionIdentity & {
    plan: Readonly<Record<string, CanonicalJSONInput>>;
    executionManifest: Readonly<Record<string, CanonicalJSONInput>>;
  }, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  renderFrame(input: AgentActionIdentity & {
    plan: Readonly<Record<string, CanonicalJSONInput>>;
    executionManifest: Readonly<Record<string, CanonicalJSONInput>>;
    frame: Readonly<Record<string, CanonicalJSONInput>>;
  }, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  submitRender(input: AgentActionIdentity & {
    job: Readonly<Record<string, CanonicalJSONInput>>;
  }, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  renderStatus(input: AgentJobAction, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  renderHistory(input: AgentJobAction & {
    afterSequence?: number | bigint;
    limit?: number | bigint;
  }, options?: { signal?: AbortSignal }): Promise<AgentResult>;
  cancelRender(input: AgentJobAction, options?: { signal?: AbortSignal }): Promise<AgentResult>;
}

export type PreviewFrameErrorCode =
  | "PREVIEW.REQUEST_INVALID"
  | "PREVIEW.VALUE_INVALID"
  | "PREVIEW.PLAYER_TOKEN_INVALID"
  | "PREVIEW.RESULT_INVALID"
  | "PREVIEW.RENDER_REJECTED"
  | "PREVIEW.RECEIPT_INVALID";

export declare class PreviewFrameError extends Error {
  readonly code: PreviewFrameErrorCode;
  constructor(code: PreviewFrameErrorCode, detail: string, cause?: unknown);
}

export interface PreviewPlanInput extends Readonly<Record<string, CanonicalJSONInput>> {
  readonly fingerprint: string;
  readonly frame_range: Readonly<{ first: IntegerLike; count: IntegerLike }>;
}

export interface PreviewExecutionManifestInput extends Readonly<Record<string, CanonicalJSONInput>> {
  readonly fingerprint: string;
  readonly plan_fingerprint: string;
  readonly backend: Readonly<{
    backend_id: string;
    build_fingerprint: string;
    readonly [name: string]: CanonicalJSONInput;
  }>;
  readonly output: Readonly<{
    reference_profile: string;
    decoded_video_profile: string;
    width: IntegerLike;
    height: IntegerLike;
    readonly [name: string]: CanonicalJSONInput;
  }>;
}

export interface ExactFrameRequestV1 {
  readonly schema_version: 1n;
  readonly id: string;
  readonly idempotency_key: string;
  readonly plan_fingerprint: string;
  readonly execution_manifest_fingerprint: string;
  readonly composition_frame_index: bigint;
  readonly artifact_uri: string;
}

export interface PreviewFrameArtifactV1 extends Readonly<Record<string, CanonicalJSONValue>> {
  readonly uri: string;
  readonly content_hash: string;
  readonly size_bytes: bigint;
  readonly decoded_content_hash: string;
  readonly plan_fingerprint: string;
  readonly execution_manifest_fingerprint: string;
  readonly backend_id: string;
  readonly backend_build_fingerprint: string;
  readonly reference_profile: string;
  readonly png_encoding_profile: "cutly.png.rgba8.ffmpeg.single-frame.v1";
  readonly decoded_video_profile: string;
  readonly width: bigint;
  readonly height: bigint;
  readonly composition_frame_index: bigint;
  readonly timeline_time: Readonly<Record<string, CanonicalJSONValue>>;
  readonly evaluation_fingerprint: string;
  readonly evaluation: Readonly<Record<string, CanonicalJSONValue>>;
}

export interface PreviewFrameResult {
  readonly result: AgentResult;
  readonly request: ExactFrameRequestV1;
  readonly artifact: PreviewFrameArtifactV1;
  readonly token: FrameRequestToken;
  readonly presented: boolean;
}

export declare function createExactFrameRequest(input: {
  id: string;
  idempotencyKey: string;
  plan: PreviewPlanInput;
  executionManifest: PreviewExecutionManifestInput;
  compositionFrameIndex: IntegerLike;
  artifactUri: string;
}): ExactFrameRequestV1;

export interface AgentPreviewAdapterConfig {
  client: Pick<CutlyClient, "renderFrame">;
  projectId: string;
  plan: PreviewPlanInput;
  executionManifest: PreviewExecutionManifestInput;
  requestIDForFrame(token: FrameRequestToken): string;
  artifactURIForFrame(token: FrameRequestToken): string;
}

export declare class AgentPreviewAdapter {
  constructor(config: AgentPreviewAdapterConfig);
  renderCurrentFrame(
    player: Pick<HeadlessPlayer, "requestFrame" | "acceptFrame">,
    options?: { signal?: AbortSignal },
  ): Promise<PreviewFrameResult>;
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

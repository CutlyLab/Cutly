// Package videodecode implements Cutly's bounded, process-isolated FFmpeg
// video-frame decoder. It consumes an immutable ProbeManifest plus a complete
// display-PTS index and returns immutable linear-light compositor surfaces.
//
// The first profile is intentionally narrow: static SDR BT.709/sRGB-family
// YUV without alpha, explicit range/chroma metadata, square-pixel
// normalization and quarter-turn rotation. RotationDegrees follows FFprobe's
// display-matrix report: positive is counterclockwise in display space.
// HDR, ICC, arbitrary rotation,
// implicit color defaults and production encoding remain fail-closed. Because
// ProbeManifest v1 cannot carry interlace, display-matrix-shape or dynamic
// per-frame color facts, Open also requires separately fingerprinted complete
// VideoSemanticsEvidence from a pinned scanner.
package videodecode

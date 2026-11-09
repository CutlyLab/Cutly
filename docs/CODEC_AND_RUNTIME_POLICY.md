# Codec and media-runtime policy

Cutly source does not bundle an unreviewed system FFmpeg. Production native
rendering requires a host-provided FFmpeg/FFprobe bundle whose manifest,
signature, executable hashes, dependency closure, and trust key are verified
before probing or rendering.

The M4 output profile is H.264 video plus AAC audio in MP4. The controlled
reference acceptance runtime also requires the declared zscale path for color
conversion. Availability of an encoder or decoder in an arbitrary system
FFmpeg is not release evidence.

Distributors are responsible for confirming that their chosen binary build,
enabled codecs, deployment geography, patent licenses, and redistribution
terms are appropriate. Cutly's Apache-2.0 license does not grant third-party
codec patent rights and does not change the license of an external runtime.

Release artifacts must identify the approved runtime bundle and retain its
own notices. Cutly's source SBOM and `deps/THIRD_PARTY_NOTICES.md` cover the Go
and packaged SDK source dependency closure; they do not claim to be the SBOM
for an independently supplied FFmpeg binary.

No caller or Agent request may select runtime trust roots, executable digests,
codec flags, dependency fingerprints, or unsafe media locators. A missing,
unsigned, drifted, or unsupported runtime fails closed.

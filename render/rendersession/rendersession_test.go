package rendersession

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	stdcolor "image/color"
	"image/png"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/referencevisual"
)

// The differential fixture is one mixed composition with an image backdrop, a second image
// entering mid-timeline and a vector dissolve transition, so the gate covers changing layer
// counts and the transition kernel, not just static frames.
func sessionFixture(t *testing.T) (documentv2.Document, []referencevisual.Binding, referencevisual.Config) {
	return sessionFixtureTB(t, 64, 48)
}

func sessionFixtureTB(t testing.TB, width, height int) (documentv2.Document, []referencevisual.Binding, referencevisual.Config) {
	t.Helper()
	backdrop := sessionPNG(t, width, height, 11)
	overlay := sessionPNG(t, width/2, height/2, 23)
	document := documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "session-document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "backdrop", Kind: documentv2.AssetKindImage, URI: "backdrop.png", ContentHash: sessionHash(backdrop)},
			{ID: "overlay", Kind: documentv2.AssetKindImage, URI: "overlay.png", ContentHash: sessionHash(overlay)},
			{ID: "red", Kind: documentv2.AssetKindVector, Vector: sessionVector("red", 65_535, 20_000, 20_000, width, height)},
			{ID: "blue", Kind: documentv2.AssetKindVector, Vector: sessionVector("blue", 20_000, 20_000, 65_535, width, height)},
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{
			{ID: "backdrop-track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{{
				ID: "backdrop-clip", AssetID: "backdrop",
				TimelineRange: sessionRange(0, 60), SourceRange: sessionRange(0, 60),
			}}},
			{ID: "overlay-track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{{
				ID: "overlay-clip", AssetID: "overlay",
				TimelineRange: sessionRange(20, 20), SourceRange: sessionRange(0, 20),
			}}},
			{ID: "vector-track", Kind: documentv2.TrackKindVector, Clips: []documentv2.Clip{
				{ID: "outgoing", AssetID: "red", TimelineRange: sessionRange(0, 32), SourceRange: sessionRange(0, 32)},
				{ID: "incoming", AssetID: "blue", TimelineRange: sessionRange(32, 28), SourceRange: sessionRange(0, 28)},
			}},
		}, Transitions: []documentv2.Transition{{
			ID: "dissolve", Profile: documentv2.TransitionDissolveV1,
			OutgoingClipID: "outgoing", IncomingClipID: "incoming",
			PreDuration: videoedit.RationalTime{Value: 4, Rate: 30}, PostDuration: videoedit.RationalTime{Value: 4, Rate: 30},
		}}}},
	}
	bindings := []referencevisual.Binding{
		{AssetID: "backdrop", EncodedPNG: backdrop},
		{AssetID: "overlay", EncodedPNG: overlay},
	}
	return document, bindings, referencevisual.DefaultConfig(width, height)
}

func sessionRange(start, duration int64) documentv2.TimeRange {
	return documentv2.TimeRange{
		Start:    videoedit.RationalTime{Value: start, Rate: 30},
		Duration: videoedit.RationalTime{Value: duration, Rate: 30},
	}
}

func sessionVector(id documentv2.ID, r, g, b uint16, width, height int) *documentv2.VectorSource {
	fill := documentv2.ColorRGBA16{R: r, G: g, B: b, A: 40_000}
	commands := []documentv2.VectorCommand{
		{Op: documentv2.VectorMoveTo, Points: []documentv2.Point{{X: documentv2.Rational{Numerator: 0, Denominator: 1}, Y: documentv2.Rational{Numerator: 0, Denominator: 1}}}},
		{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: documentv2.Rational{Numerator: int64(width), Denominator: 1}, Y: documentv2.Rational{Numerator: 0, Denominator: 1}}}},
		{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: documentv2.Rational{Numerator: int64(width), Denominator: 1}, Y: documentv2.Rational{Numerator: int64(height), Denominator: 1}}}},
		{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: documentv2.Rational{Numerator: 0, Denominator: 1}, Y: documentv2.Rational{Numerator: int64(height), Denominator: 1}}}},
		{Op: documentv2.VectorClose},
	}
	return &documentv2.VectorSource{
		Profile: documentv2.VectorProfilePathViewportPXV2,
		ViewBox: documentv2.Rect{
			X: documentv2.Rational{Numerator: 0, Denominator: 1}, Y: documentv2.Rational{Numerator: 0, Denominator: 1},
			Width: documentv2.Rational{Numerator: int64(width), Denominator: 1}, Height: documentv2.Rational{Numerator: int64(height), Denominator: 1},
		},
		Viewport: &documentv2.RasterViewport{WidthPixels: uint32(width), HeightPixels: uint32(height)},
		Paths:    []documentv2.VectorPath{{ID: id + "-path", FillRule: documentv2.VectorFillNonZero, Fill: &fill, Commands: commands}},
	}
}

// sessionPNG encodes one deterministic strict-profile PNG (IHDR + injected sRGB, alpha below
// 255 so the encoder keeps the required truecolor-with-alpha type).
func sessionPNG(t testing.TB, width, height int, seed byte) []byte {
	t.Helper()
	value := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			value.SetNRGBA(x, y, stdcolor.NRGBA{R: seed + byte(x), G: seed + byte(y), B: seed*2 + byte(x/3), A: 250})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, value); err != nil {
		t.Fatal(err)
	}
	encodedBytes := encoded.Bytes()
	length := int(binary.BigEndian.Uint32(encodedBytes[8:12]))
	end := 8 + 12 + length
	chunk := make([]byte, 13)
	binary.BigEndian.PutUint32(chunk[:4], 1)
	copy(chunk[4:8], "sRGB")
	chunk[8] = 0
	binary.BigEndian.PutUint32(chunk[9:], crc32.ChecksumIEEE(chunk[4:9]))
	result := make([]byte, 0, len(encodedBytes)+len(chunk))
	result = append(result, encodedBytes[:end]...)
	result = append(result, chunk...)
	return append(result, encodedBytes[end:]...)
}

func sessionHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// frameTimes covers both layer-count boundaries, the dissolve transition window and the tail.
func frameTimes() []documentv2.RationalTime {
	frames := []int64{0, 1, 19, 20, 21, 28, 29, 30, 31, 32, 33, 40, 55, 59}
	times := make([]documentv2.RationalTime, 0, len(frames))
	for _, frame := range frames {
		times = append(times, videoedit.RationalTime{Value: frame, Rate: 30})
	}
	return times
}

// TestSessionMatchesReferenceByteForByte is the differential gate: every served frame must be
// byte-identical to the complete reference export at the pixel, output and PNG boundaries.
func TestSessionMatchesReferenceByteForByte(t *testing.T) {
	document, bindings, config := sessionFixture(t)
	session, err := Open(context.Background(), document, bindings, config, Options{DeepValidationInterval: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, at := range frameTimes() {
		produced, err := session.RenderFrame(context.Background(), "timeline", at)
		if err != nil {
			t.Fatalf("session frame %d: %v", at.Value, err)
		}
		expected, err := session.referenceFrame(t, at)
		if err != nil {
			t.Fatalf("reference frame %d: %v", at.Value, err)
		}
		if produced.Visual.PixelHash != expected.Visual.PixelHash ||
			produced.Output.PixelHash != expected.Output.PixelHash ||
			produced.PNG.ArtifactHash != expected.PNG.ArtifactHash {
			t.Fatalf("frame %d diverged: visual %s vs %s, png %s vs %s",
				at.Value, produced.Visual.PixelHash, expected.Visual.PixelHash, produced.PNG.ArtifactHash, expected.PNG.ArtifactHash)
		}
	}
	if session.DeepValidations() == 0 {
		t.Fatal("deep validation never ran")
	}
}

func (session *Session) referenceFrame(t *testing.T, at documentv2.RationalTime) (FrameResult, error) {
	t.Helper()
	// The session's own reference program is the arbiter: full validation, replay included.
	result, err := session.reference.RenderAt(context.Background(), "timeline", at)
	if err != nil {
		return FrameResult{}, err
	}
	return FrameResult{
		Visual: result.Visual, Output: result.Output, PNG: result.PNG,
	}, nil
}

// TestSessionDeterminismAcrossTenRuns is the repeatability gate: ten complete session renders
// must produce identical artifacts for every frame.
func TestSessionDeterminismAcrossTenRuns(t *testing.T) {
	document, bindings, config := sessionFixture(t)
	times := frameTimes()
	first := make([]string, len(times))
	for run := 0; run < 10; run++ {
		session, err := Open(context.Background(), document, bindings, config, Options{})
		if err != nil {
			t.Fatal(err)
		}
		for index, at := range times {
			frame, err := session.RenderFrame(context.Background(), "timeline", at)
			if err != nil {
				t.Fatalf("run %d frame %d: %v", run, at.Value, err)
			}
			if run == 0 {
				first[index] = frame.PNG.ArtifactHash
			} else if frame.PNG.ArtifactHash != first[index] {
				t.Fatalf("run %d frame %d artifact %s != %s", run, at.Value, frame.PNG.ArtifactHash, first[index])
			}
		}
		session.Close()
	}
}

func TestSessionRejectsInvalidUseAndCancellation(t *testing.T) {
	document, bindings, config := sessionFixture(t)
	session, err := Open(context.Background(), document, bindings, config, Options{})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := session.RenderFrame(canceled, "timeline", videoedit.RationalTime{Value: 1, Rate: 30}); err == nil {
		t.Fatal("canceled render succeeded")
	}
	if _, err := session.RenderSurface(canceled, "timeline", videoedit.RationalTime{Value: 1, Rate: 30}); err == nil {
		t.Fatal("canceled surface render succeeded")
	}
	// The session stays usable after a canceled call.
	if _, err := session.RenderFrame(context.Background(), "timeline", videoedit.RationalTime{Value: 1, Rate: 30}); err != nil {
		t.Fatalf("render after cancellation: %v", err)
	}
	if _, err := session.RenderFrame(context.Background(), "missing-timeline", videoedit.RationalTime{Value: 1, Rate: 30}); err == nil {
		t.Fatal("unknown timeline rendered")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.RenderFrame(context.Background(), "timeline", videoedit.RationalTime{Value: 1, Rate: 30}); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("render after close = %v", err)
	}
	if _, err := Open(canceled, document, bindings, config, Options{}); err == nil {
		t.Fatal("open with canceled context succeeded")
	}
	if _, err := Open(context.Background(), document, bindings, config, Options{DeepValidationInterval: -1}); err == nil {
		t.Fatal("negative deep interval accepted")
	}
}

// TestSessionConcurrentFramesMatchSerialRender locks the concurrency contract: parallel frame
// renders produce the same artifacts as serial ones.
func TestSessionConcurrentFramesMatchSerialRender(t *testing.T) {
	document, bindings, config := sessionFixture(t)
	session, err := Open(context.Background(), document, bindings, config, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	times := frameTimes()
	serial := make([]string, len(times))
	for index, at := range times {
		frame, err := session.RenderFrame(context.Background(), "timeline", at)
		if err != nil {
			t.Fatal(err)
		}
		serial[index] = frame.PNG.ArtifactHash
	}
	parallel := make([]string, len(times))
	var wait sync.WaitGroup
	for index, at := range times {
		wait.Add(1)
		go func(index int, at documentv2.RationalTime) {
			defer wait.Done()
			frame, err := session.RenderFrame(context.Background(), "timeline", at)
			if err != nil {
				t.Errorf("parallel frame %d: %v", at.Value, err)
				return
			}
			parallel[index] = frame.PNG.ArtifactHash
		}(index, at)
	}
	wait.Wait()
	for index := range serial {
		if serial[index] != parallel[index] {
			t.Fatalf("parallel frame %d diverged: %s != %s", times[index].Value, parallel[index], serial[index])
		}
	}
}

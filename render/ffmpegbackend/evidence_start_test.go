package ffmpegbackend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/evidence"
)

func TestDecodedEvidenceStartPreservesFailureCause(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"canceled", "deadline", "missing executable"} {
		t.Run(name, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "encoded")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			ctx := context.Background()
			var want error
			path := executable
			state := renderapi.JobFailed
			switch name {
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want, state = context.Canceled, renderapi.JobCanceled
			case "deadline":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Unix(1, 0))
				defer cancel()
				want, state = context.DeadlineExceeded, renderapi.JobCanceled
			default:
				path = filepath.Join(t.TempDir(), "missing-executable")
			}
			backend := &Backend{executable: path, maxStderrBytes: 1024}
			sequence, size, err := backend.decodeSequence(ctx, decodedRequest{file: file, unit: "frames", unitBytes: 4, expectedCount: 1})
			if !errors.Is(err, ErrCommand) || (want != nil && !errors.Is(err, want)) {
				t.Fatalf("start error = %v, want ErrCommand and %v", err, want)
			}
			if size != 0 || !reflect.DeepEqual(sequence, evidence.DecodedSequence{}) {
				t.Fatal("failed start returned decoded evidence")
			}
			var events []renderapi.ProgressEvent
			emitter := progressEmitter{jobID: "decoded-start", sink: renderapi.EventSinkFunc(func(_ context.Context, event renderapi.ProgressEvent) error {
				events = append(events, event)
				return nil
			})}
			terminalCause := want
			if terminalCause == nil {
				terminalCause = ErrCommand
			}
			if got := emitter.fail(ctx, CodePublicationRejected, err); !errors.Is(got, terminalCause) {
				t.Fatalf("terminal error = %v", got)
			}
			if len(events) != 1 || events[0].State != state {
				t.Fatalf("terminal events = %+v, want %s", events, state)
			}
		})
	}
}

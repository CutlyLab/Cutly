package ffmpegbackend

import (
	"context"
	"errors"
	"testing"

	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestM1KilledDecoderFailsClosedWithoutPublication(t *testing.T) {
	if !platformSupportsSecureDescriptors() {
		t.Skip("M1 secure render profile is unavailable on this platform")
	}
	fixture := newBackendFixture(t, fakeKilledDecoderScript)
	var events []renderapi.ProgressEvent
	_, err := fixture.backend.Render(context.Background(), fixture.request, renderapi.EventSinkFunc(func(_ context.Context, event renderapi.ProgressEvent) error {
		events = append(events, event)
		return nil
	}))
	if !errors.Is(err, ErrCommand) {
		t.Fatalf("killed decoder error = %v, want ErrCommand", err)
	}
	if len(events) == 0 || events[len(events)-1].State != renderapi.JobFailed {
		t.Fatalf("terminal events = %+v", events)
	}
	assertOutputRootEmpty(t, fixture.outputRoot)
}

const fakeKilledDecoderScript = `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "ffmpeg version cutly-fake-1"
  exit 0
fi
case " $* " in
  *" -f rawvideo "*|*" -f s16le "*)
    printf 'partial decoded bytes'
    kill -9 $$
    ;;
esac
descriptor=""
previous=""
for last do
  if [ "$previous" = "-fd" ]; then descriptor="$last"; fi
  previous="$last"
done
case "$last" in
  fd:*) eval "printf 'fake mp4' >&$descriptor" ;;
  *) printf 'fake mp4' > "$last" ;;
esac
`

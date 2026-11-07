package buildidentity

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type providerPanicContext struct {
	context.Context
	calls, panicAt int
}

func (c *providerPanicContext) Err() error {
	c.calls++
	if c.calls == c.panicAt {
		panic("provider context failure")
	}
	return nil
}

func TestProviderMeasurementContextPanicFailsZero(t *testing.T) {
	data := bytes.Repeat([]byte("never executed\n"), providerMeasurementBufferBytes)
	path := providerFixture(t, data)
	development := providerTestPolicy(t, ProviderDistributionDevelopment, "")
	packaged := providerTestPolicy(t, ProviderDistributionPackaged, providerTestDigest(data))
	// Entry check, ancestor checks, first read check, then second read check.
	parts := strings.Split(strings.TrimPrefix(path, filepath.VolumeName(path)+string(filepath.Separator)), string(filepath.Separator))
	for name, measure := range map[string]func(context.Context, string) (ProviderExecutableIdentity, error){
		"development": development.MeasureLocalDevelopmentExecutable,
		"packaged":    packaged.MeasurePackagedExecutable,
	} {
		for _, stage := range []struct {
			name string
			at   int
		}{{"entry", 1}, {"mid-read", len(parts) + 2}} {
			t.Run(name+"/"+stage.name, func(t *testing.T) {
				ctx := &providerPanicContext{Context: context.Background(), panicAt: stage.at}
				identity, err := measure(ctx, path)
				requireZeroProviderIdentity(t, identity, err)
				if ctx.calls != stage.at || !errors.Is(err, ErrProviderExecutableInvalid) {
					t.Fatalf("panic not contained at expected boundary: calls=%d error=%v", ctx.calls, err)
				}
			})
		}
	}
}

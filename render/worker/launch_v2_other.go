//go:build !darwin && !linux

package worker

import (
	"context"
	"os"
)

func WatchParentV2(context.Context, context.CancelFunc, *os.File) (func(), error) {
	return nil, ErrUnsupported
}

func checkLaunchDescriptorV2(*os.File) error        { return ErrUnsupported }
func openPrivateRegularV2(string) (*os.File, error) { return nil, ErrUnsupported }

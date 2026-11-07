//go:build darwin

package buildidentity

import (
	"fmt"
	"os"
	"syscall"
)

const providerMeasurementSupported = true
const providerExecutablePermissionBits = true

// Darwin's O_NOFOLLOW_ANY is a kernel no-symlinks-anywhere guarantee. It is not
// exported by Go's frozen syscall package (XNU sys/fcntl.h defines this value).
// O_EVTONLY obtains metadata without opening a hostile device for reading.
// After validating regular/executable/bounded metadata, /dev/fd duplicates the
// already selected descriptor rather than resolving the original path again.
func openProviderExecutable(path string) (*os.File, error) {
	const noFollowAny = 0x20000000
	metadata, err := os.OpenFile(path, syscall.O_EVTONLY|syscall.O_NONBLOCK|noFollowAny, 0)
	if err != nil {
		return nil, err
	}
	info, err := metadata.Stat()
	if err == nil {
		err = validateProviderExecutableInfo(info)
	}
	if err != nil {
		_ = metadata.Close()
		return nil, err
	}
	file, err := os.OpenFile(fmt.Sprintf("/dev/fd/%d", metadata.Fd()), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	closeErr := metadata.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		_ = file.Close()
		return nil, closeErr
	}
	return file, nil
}

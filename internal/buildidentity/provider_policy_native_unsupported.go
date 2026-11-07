//go:build !linux && !darwin

package buildidentity

import "os"

// Protocol portability does not imply native provider authority availability.
// In particular Windows and wasm compile, but measurement fails closed until
// descriptor-bound, no-follow filesystem support has a real-platform gate.
const providerMeasurementSupported = false
const providerExecutablePermissionBits = false

func openProviderExecutable(string) (*os.File, error) {
	return nil, ErrProviderMeasurementUnsupported
}

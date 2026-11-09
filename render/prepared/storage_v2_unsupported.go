//go:build !darwin && !linux

package prepared

import "context"

func storageSupportedV2() bool { return false }
func (s *Store) withStorageV2(context.Context, bool, func(storageSessionV2) error) error {
	return ErrUnsupported
}

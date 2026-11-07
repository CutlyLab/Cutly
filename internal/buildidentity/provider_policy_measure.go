package buildidentity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const providerMeasurementBufferBytes = 64 * 1024

type providerDirectory struct {
	name   string
	before os.FileInfo
}

// Root alone follows in-root symlinks, so it is used only for metadata
// snapshots anchored at the filesystem root. Native openProviderExecutable
// implementations enforce no-follow for the actual executable selection and
// obtain metadata-only descriptors before opening regular files for reading.
// Ancestor and file identity snapshots also reject observed path replacements.
// Platforms without those native guarantees fail closed.
func measureProviderExecutable(ctx context.Context, path string) (identity ProviderExecutableIdentity, err error) {
	if ctx == nil {
		return identity, fmt.Errorf("%w: nil context", ErrProviderExecutableInvalid)
	}
	if err := ctx.Err(); err != nil {
		return identity, err
	}
	if !providerMeasurementSupported {
		return identity, ErrProviderMeasurementUnsupported
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.IndexByte(path, 0) >= 0 {
		return identity, fmt.Errorf("%w: host must select a clean absolute path", ErrProviderExecutableInvalid)
	}
	volume := filepath.VolumeName(path)
	// Network shares and Windows device namespace paths are not local host
	// executable selections. In particular do not open named pipe namespaces.
	if strings.HasPrefix(volume, `\\`) {
		return identity, fmt.Errorf("%w: nonlocal volume", ErrProviderExecutableInvalid)
	}
	anchor := volume + string(filepath.Separator)
	parts := strings.Split(path[len(anchor):], string(filepath.Separator))
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return identity, fmt.Errorf("%w: missing executable name", ErrProviderExecutableInvalid)
	}
	root, err := os.OpenRoot(anchor)
	if err != nil {
		return identity, err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil && err == nil {
			identity, err = ProviderExecutableIdentity{}, closeErr
		}
	}()
	directories := make([]providerDirectory, 0, len(parts)-1)
	for index := range parts[:len(parts)-1] {
		if err := ctx.Err(); err != nil {
			return ProviderExecutableIdentity{}, err
		}
		name := filepath.Join(parts[:index+1]...)
		before, err := root.Lstat(name)
		if err != nil {
			return ProviderExecutableIdentity{}, err
		}
		if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			return ProviderExecutableIdentity{}, fmt.Errorf("%w: ancestor is not a nonsymlink directory", ErrProviderExecutableInvalid)
		}
		directories = append(directories, providerDirectory{name: name, before: before})
	}
	name := filepath.Join(parts...)
	before, err := root.Lstat(name)
	if err != nil {
		return ProviderExecutableIdentity{}, err
	}
	if err := validateProviderExecutableInfo(before); err != nil {
		return ProviderExecutableIdentity{}, err
	}
	file, err := openProviderExecutable(path)
	if err != nil {
		return ProviderExecutableIdentity{}, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			identity, err = ProviderExecutableIdentity{}, closeErr
		}
	}()
	opened, err := file.Stat()
	if err != nil {
		return ProviderExecutableIdentity{}, err
	}
	if !sameProviderFile(before, opened) {
		return ProviderExecutableIdentity{}, ErrProviderExecutableChanged
	}
	// Bounds and regular/executable checks precede allocation and reads.
	if err := validateProviderExecutableInfo(opened); err != nil {
		return ProviderExecutableIdentity{}, err
	}
	hash := sha256.New()
	buffer := make([]byte, providerMeasurementBufferBytes)
	var measured int64
	for {
		if err := ctx.Err(); err != nil {
			return ProviderExecutableIdentity{}, err
		}
		// Read at most the original size plus one byte, even if the file grows.
		remaining := opened.Size() - measured + 1
		chunk := buffer
		if remaining < int64(len(chunk)) {
			chunk = chunk[:int(remaining)]
		}
		n, readErr := file.Read(chunk)
		measured += int64(n)
		if measured > opened.Size() {
			return ProviderExecutableIdentity{}, ErrProviderExecutableChanged
		}
		_, _ = hash.Write(chunk[:n]) // SHA-256 Write never fails.
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return ProviderExecutableIdentity{}, readErr
		}
		if n == 0 {
			return ProviderExecutableIdentity{}, io.ErrNoProgress
		}
	}
	if measured != opened.Size() {
		return ProviderExecutableIdentity{}, ErrProviderExecutableChanged
	}
	after, err := file.Stat()
	if err != nil {
		return ProviderExecutableIdentity{}, err
	}
	entry, err := root.Lstat(name)
	if err != nil {
		return ProviderExecutableIdentity{}, err
	}
	if !sameProviderFile(opened, after) || !sameProviderFile(opened, entry) {
		return ProviderExecutableIdentity{}, ErrProviderExecutableChanged
	}
	for _, directory := range directories {
		if err := directory.validate(root); err != nil {
			return ProviderExecutableIdentity{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return ProviderExecutableIdentity{}, err
	}
	return ProviderExecutableIdentity{digest: "sha256:" + hex.EncodeToString(hash.Sum(nil))}, nil
}

func validateProviderExecutableInfo(info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxProviderExecutableBytes {
		return fmt.Errorf("%w: requires regular file with positive bounded size", ErrProviderExecutableInvalid)
	}
	if providerExecutablePermissionBits && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%w: no executable permission bits", ErrProviderExecutableInvalid)
	}
	return nil
}

func sameProviderFile(before, after os.FileInfo) bool {
	return os.SameFile(before, after) && before.Size() == after.Size() &&
		before.Mode() == after.Mode() && before.ModTime().Equal(after.ModTime())
}

func (d providerDirectory) validate(root *os.Root) error {
	entry, err := root.Lstat(d.name)
	if err != nil {
		return err
	}
	if !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(d.before, entry) || d.before.Mode() != entry.Mode() {
		return ErrProviderExecutableChanged
	}
	return nil
}

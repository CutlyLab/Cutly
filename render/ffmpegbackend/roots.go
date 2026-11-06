package ffmpegbackend

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type authorizedRoot struct {
	lexical  string
	resolved string
	handle   *os.Root
}

type authorizedInput struct {
	root     authorizedRoot
	relative string
}

func resolveInputRoots(values []string) ([]authorizedRoot, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("%w: at least one input root is required", ErrConfiguration)
	}
	roots := make([]authorizedRoot, 0, len(values))
	success := false
	defer func() {
		if !success {
			for _, root := range roots {
				_ = root.handle.Close()
			}
		}
	}()
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%w: input root is blank", ErrConfiguration)
		}
		lexical, err := filepath.Abs(value)
		if err != nil {
			return nil, fmt.Errorf("%w: input root: %v", ErrConfiguration, err)
		}
		lexical = filepath.Clean(lexical)
		info, err := os.Lstat(lexical)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: input root %q must be an existing non-symlink directory", ErrConfiguration, lexical)
		}
		resolved, err := filepath.EvalSymlinks(lexical)
		if err != nil {
			return nil, fmt.Errorf("%w: resolve input root %q: %v", ErrConfiguration, lexical, err)
		}
		resolved = filepath.Clean(resolved)
		if _, duplicate := seen[lexical+"\x00"+resolved]; duplicate {
			continue
		}
		seen[lexical+"\x00"+resolved] = struct{}{}
		handle, err := os.OpenRoot(resolved)
		if err != nil {
			return nil, fmt.Errorf("%w: open input root %q: %v", ErrConfiguration, resolved, err)
		}
		pathInfo, pathErr := os.Lstat(resolved)
		handleInfo, handleErr := handle.Stat(".")
		if pathErr != nil || handleErr != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(pathInfo, handleInfo) {
			_ = handle.Close()
			return nil, fmt.Errorf("%w: input root %q changed while it was authorized", ErrConfiguration, resolved)
		}
		roots = append(roots, authorizedRoot{lexical: lexical, resolved: resolved, handle: handle})
	}
	success = true
	return roots, nil
}

// authorizeInput confines a locator to one configured root and rejects every symlink component
// below that root. System-level aliases above the configured root (for example /var -> /private/var
// on macOS) are resolved once when the backend is constructed.
func (backend *Backend) authorizeInput(value string) (authorizedInput, error) {
	if backend == nil || !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.IndexByte(value, 0) >= 0 {
		return authorizedInput{}, fmt.Errorf("%w: input path must be absolute and clean", ErrContent)
	}
	for _, root := range backend.inputRoots {
		for _, base := range []string{root.lexical, root.resolved} {
			relative, err := filepath.Rel(base, value)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				continue
			}
			current := base
			if relative != "." {
				for _, component := range strings.Split(relative, string(filepath.Separator)) {
					current = filepath.Join(current, component)
					componentRelative, relErr := filepath.Rel(base, current)
					if relErr != nil {
						return authorizedInput{}, fmt.Errorf("%w: inspect input %q: %v", ErrContent, value, relErr)
					}
					info, statErr := root.handle.Lstat(filepath.ToSlash(componentRelative))
					if statErr != nil {
						return authorizedInput{}, fmt.Errorf("%w: inspect input %q: %v", ErrContent, value, statErr)
					}
					if info.Mode()&os.ModeSymlink != 0 {
						return authorizedInput{}, fmt.Errorf("%w: input %q traverses a symlink", ErrContent, value)
					}
				}
			}
			return authorizedInput{root: root, relative: filepath.ToSlash(relative)}, nil
		}
	}
	return authorizedInput{}, fmt.Errorf("%w: input %q is outside configured roots", ErrContent, value)
}

func (backend *Backend) createOutputTemp(prefix, suffix string) (*os.File, string, error) {
	if backend == nil || backend.outputHandle == nil {
		return nil, "", fmt.Errorf("%w: output root is closed", ErrConfiguration)
	}
	for attempt := 0; attempt < 64; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", err
		}
		name := prefix + hex.EncodeToString(random[:]) + suffix
		file, err := backend.outputHandle.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, name, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("%w: cannot allocate a unique output temporary", ErrPublication)
}

func (backend *Backend) createOutputTempDir(prefix string) (*os.Root, string, error) {
	for attempt := 0; attempt < 64; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", err
		}
		name := prefix + hex.EncodeToString(random[:])
		if err := backend.outputHandle.Mkdir(name, 0o700); err != nil {
			if os.IsExist(err) {
				continue
			}
			return nil, "", err
		}
		root, err := backend.outputHandle.OpenRoot(name)
		if err != nil {
			_ = backend.outputHandle.Remove(name)
			return nil, "", err
		}
		pathInfo, pathErr := backend.outputHandle.Lstat(name)
		handleInfo, handleErr := root.Stat(".")
		if pathErr != nil || handleErr != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(pathInfo, handleInfo) {
			_ = root.Close()
			_ = backend.outputHandle.Remove(name)
			return nil, "", fmt.Errorf("%w: staging directory changed while opening its handle", ErrPublication)
		}
		return root, name, nil
	}
	return nil, "", fmt.Errorf("%w: cannot allocate a unique staging directory", ErrPublication)
}

func (backend *Backend) verifyOutputPathIdentity() error {
	if backend == nil || backend.outputHandle == nil {
		return fmt.Errorf("%w: output root is closed", ErrConfiguration)
	}
	pathInfo, err := os.Lstat(backend.outputRoot)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: output root path is unavailable", ErrConfiguration)
	}
	handleInfo, err := backend.outputHandle.Stat(".")
	if err != nil || !os.SameFile(pathInfo, handleInfo) {
		return fmt.Errorf("%w: output root path no longer names the authorized directory", ErrConfiguration)
	}
	return nil
}

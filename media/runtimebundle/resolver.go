package runtimebundle

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func runtimeRootSecurityUnsupported() bool {
	return runtime.GOOS == "js" || runtime.GOOS == "plan9"
}

// Resolve verifies one local bundle. Signature and fingerprint verification
// always precede closure enumeration and content hashing.
func (resolver *Resolver) Resolve(ctx context.Context, bundleRoot string) (Resolution, error) {
	if resolver == nil || ctx == nil {
		return Resolution{}, ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return Resolution{}, err
	}
	if runtimeRootSecurityUnsupported() {
		return Resolution{}, ErrPlatform
	}
	if bundleRoot == "" || !filepath.IsAbs(bundleRoot) || filepath.Clean(bundleRoot) != bundleRoot {
		return Resolution{}, fmt.Errorf("%w: bundle root", ErrClosure)
	}
	absolute := bundleRoot
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil || filepath.Clean(physical) != absolute {
		return Resolution{}, errors.Join(ErrClosure, err)
	}
	before, err := os.Lstat(absolute)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || !secureDirectoryMode(before) {
		return Resolution{}, errors.Join(ErrClosure, err)
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return Resolution{}, errors.Join(ErrClosure, err)
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		return Resolution{}, errors.Join(ErrClosure, err)
	}

	raw, manifestIdentity, err := readRegularFile(ctx, root, resolver.manifestName, resolver.maxManifestBytes)
	if err != nil {
		return Resolution{}, fmt.Errorf("%w: manifest: %v", ErrClosure, err)
	}
	manifest, err := DecodeManifest(raw)
	if err != nil {
		return Resolution{}, err
	}
	if err := validateManifestShape(manifest, resolver.maxFiles, resolver.maxTotalFileBytes); err != nil {
		return Resolution{}, err
	}
	if manifest.Platform != resolver.platform || manifest.Architecture != resolver.architecture {
		return Resolution{}, ErrPlatform
	}
	publicKey, trusted := resolver.trustedKeys[manifest.KeyID]
	if !trusted {
		return Resolution{}, ErrTrust
	}
	wantFingerprint, err := payloadFingerprint(manifest)
	if err != nil {
		return Resolution{}, err
	}
	if wantFingerprint != manifest.BundleFingerprint {
		return Resolution{}, fmt.Errorf("%w: bundle fingerprint", ErrSignature)
	}
	preimage, err := signaturePreimage(manifest)
	if err != nil {
		return Resolution{}, err
	}
	signature, err := hex.DecodeString(manifest.Signature)
	if err != nil || !ed25519.Verify(publicKey, preimage, signature) {
		return Resolution{}, ErrSignature
	}
	if err := ctx.Err(); err != nil {
		return Resolution{}, err
	}

	if err := verifyCompleteClosure(ctx, root, resolver.manifestName, manifest, resolver.maxClosureNodes, nil); err != nil {
		return Resolution{}, err
	}
	verifiedFiles := make(map[string]os.FileInfo, len(manifest.Files)+1)
	verifiedFiles[resolver.manifestName] = manifestIdentity
	for _, file := range manifest.Files {
		identity, err := verifyFile(ctx, root, file)
		if err != nil {
			return Resolution{}, err
		}
		verifiedFiles[file.Path] = identity
	}
	// Repeat the shape walk after hashing so removal/addition during verification
	// cannot be silently accepted as the observed closure.
	if err := verifyCompleteClosure(ctx, root, resolver.manifestName, manifest, resolver.maxClosureNodes, verifiedFiles); err != nil {
		return Resolution{}, err
	}
	if err := verifyRootPathIdentity(absolute, before, root); err != nil {
		return Resolution{}, err
	}
	ffmpegClosure, err := closureFingerprint(manifest, ToolFFmpeg)
	if err != nil {
		return Resolution{}, err
	}
	ffprobeClosure, err := closureFingerprint(manifest, ToolFFprobe)
	if err != nil {
		return Resolution{}, err
	}
	ffmpegEntry, _ := fileByPath(manifest.Files, manifest.FFmpegPath)
	ffprobeEntry, _ := fileByPath(manifest.Files, manifest.FFprobePath)
	frozen, err := freezeManifest(manifest)
	if err != nil {
		return Resolution{}, err
	}
	return Resolution{
		BundleRoot: absolute, Manifest: frozen, BundleFingerprint: manifest.BundleFingerprint,
		FFmpeg: Executable{
			Path:         filepath.Join(absolute, filepath.FromSlash(manifest.FFmpegPath)),
			RelativePath: manifest.FFmpegPath, BinaryContentHash: ffmpegEntry.ContentHash,
			ClosureFingerprint: ffmpegClosure,
		},
		FFprobe: Executable{
			Path:         filepath.Join(absolute, filepath.FromSlash(manifest.FFprobePath)),
			RelativePath: manifest.FFprobePath, BinaryContentHash: ffprobeEntry.ContentHash,
			ClosureFingerprint: ffprobeClosure,
		},
	}, nil
}

func verifyRootPathIdentity(absolute string, expected os.FileInfo, root *os.Root) error {
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil || filepath.Clean(physical) != absolute {
		return errors.Join(ErrClosure, err)
	}
	current, err := os.Lstat(absolute)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 ||
		!secureDirectoryMode(current) || !os.SameFile(expected, current) {
		return errors.Join(ErrClosure, err)
	}
	opened, err := root.Stat(".")
	if err != nil || !opened.IsDir() || !secureDirectoryMode(opened) || !os.SameFile(expected, opened) {
		return errors.Join(ErrClosure, err)
	}
	return nil
}

func secureDirectoryMode(information os.FileInfo) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	return information.Mode().Perm()&0o022 == 0 &&
		information.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0
}

func freezeManifest(manifest Manifest) (Manifest, error) {
	canonical, err := CanonicalManifest(manifest)
	if err != nil {
		return Manifest{}, err
	}
	return DecodeManifest(canonical)
}

func verifyFile(ctx context.Context, root *os.Root, expected FileEntry) (os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	before, err := root.Lstat(expected.Path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		unsafeSpecialMode(before.Mode()) || before.Size() != expected.SizeBytes || uint32(before.Mode().Perm()) != expected.Mode {
		return nil, fmt.Errorf("%w: file metadata %q", ErrContent, expected.Path)
	}
	file, err := root.Open(expected.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: open %q", ErrContent, expected.Path)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) ||
		unsafeSpecialMode(opened.Mode()) || opened.Size() != expected.SizeBytes || uint32(opened.Mode().Perm()) != expected.Mode {
		return nil, fmt.Errorf("%w: opened metadata %q", ErrContent, expected.Path)
	}
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	var read int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			read += int64(count)
			if read > expected.SizeBytes {
				return nil, fmt.Errorf("%w: file grew %q", ErrContent, expected.Path)
			}
			_, _ = hash.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("%w: read %q", ErrContent, expected.Path)
		}
	}
	after, err := file.Stat()
	actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if err != nil || read != expected.SizeBytes || after.Size() != expected.SizeBytes || !os.SameFile(opened, after) ||
		unsafeSpecialMode(after.Mode()) || uint32(after.Mode().Perm()) != expected.Mode || actual != expected.ContentHash {
		return nil, fmt.Errorf("%w: bytes %q", ErrContent, expected.Path)
	}
	return after, nil
}

func unsafeSpecialMode(mode os.FileMode) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	return mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0
}

func readRegularFile(ctx context.Context, root *os.Root, name string, maximum int64) ([]byte, os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	before, err := root.Lstat(name)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		unsafeSpecialMode(before.Mode()) || !secureRegularFileMode(before.Mode()) ||
		before.Size() < 1 || before.Size() > maximum {
		return nil, nil, errors.Join(ErrLimit, err)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || unsafeSpecialMode(opened.Mode()) ||
		!secureRegularFileMode(opened.Mode()) || !os.SameFile(before, opened) || opened.Size() != before.Size() {
		return nil, nil, ErrClosure
	}
	raw := make([]byte, 0, int(opened.Size()))
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if int64(len(raw))+int64(count) > maximum {
				return nil, nil, ErrLimit
			}
			raw = append(raw, buffer[:count]...)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, nil, readErr
		}
	}
	if int64(len(raw)) != opened.Size() {
		return nil, nil, ErrClosure
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(opened, after) || after.Size() != int64(len(raw)) ||
		unsafeSpecialMode(after.Mode()) || !secureRegularFileMode(after.Mode()) || after.Mode().Perm() != opened.Mode().Perm() {
		return nil, nil, ErrClosure
	}
	return raw, after, nil
}

func secureRegularFileMode(mode os.FileMode) bool {
	return runtime.GOOS == "windows" || mode.Perm()&0o022 == 0
}

func verifyCompleteClosure(
	ctx context.Context,
	root *os.Root,
	manifestName string,
	manifest Manifest,
	maximumNodes int,
	verified map[string]os.FileInfo,
) error {
	files := make(map[string]struct{}, len(manifest.Files)+1)
	directories := map[string]struct{}{".": {}}
	for _, file := range manifest.Files {
		if file.Path == manifestName {
			return fmt.Errorf("%w: manifest is a closure member", ErrManifest)
		}
		files[file.Path] = struct{}{}
		addParentDirectories(directories, file.Path)
	}
	files[manifestName] = struct{}{}
	addParentDirectories(directories, manifestName)
	if len(files)+len(directories) > maximumNodes {
		return fmt.Errorf("%w: closure path nodes", ErrLimit)
	}
	seen := make(map[string]struct{}, len(files))
	if verified != nil && len(verified) != len(files) {
		return fmt.Errorf("%w: verified file identities", ErrClosure)
	}
	state := closureWalkState{
		files: files, directories: directories, seen: seen, verified: verified, maximumNodes: maximumNodes,
	}
	if err := walkClosure(ctx, root, ".", &state); err != nil {
		return err
	}
	if len(seen) != len(files) {
		return fmt.Errorf("%w: missing file", ErrClosure)
	}
	return nil
}

type closureWalkState struct {
	files        map[string]struct{}
	directories  map[string]struct{}
	seen         map[string]struct{}
	verified     map[string]os.FileInfo
	visitedNodes int
	maximumNodes int
}

func walkClosure(ctx context.Context, root *os.Root, directory string, state *closureWalkState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	before, err := root.Lstat(directory)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() || !secureDirectoryMode(before) {
		return fmt.Errorf("%w: directory %q", ErrClosure, directory)
	}
	handle, err := root.Open(directory)
	if err != nil {
		return fmt.Errorf("%w: open directory %q", ErrClosure, directory)
	}
	opened, statErr := handle.Stat()
	if statErr != nil || !opened.IsDir() || !secureDirectoryMode(opened) || !os.SameFile(before, opened) {
		handle.Close()
		return fmt.Errorf("%w: directory identity %q", ErrClosure, directory)
	}
	var allEntries []os.DirEntry
	for {
		entries, readErr := handle.ReadDir(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			handle.Close()
			return fmt.Errorf("%w: enumerate directory %q", ErrClosure, directory)
		}
		if len(allEntries) > state.maximumNodes-len(entries) {
			handle.Close()
			return fmt.Errorf("%w: observed directory entries", ErrLimit)
		}
		allEntries = append(allEntries, entries...)
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("%w: close directory %q", ErrClosure, directory)
	}
	for _, entry := range allEntries {
		if err := ctx.Err(); err != nil {
			return err
		}
		state.visitedNodes++
		if state.visitedNodes > state.maximumNodes {
			return fmt.Errorf("%w: observed closure nodes", ErrLimit)
		}
		name := entry.Name()
		if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") {
			return fmt.Errorf("%w: invalid directory entry", ErrClosure)
		}
		relative := name
		if directory != "." {
			relative = directory + "/" + name
		}
		information, err := root.Lstat(relative)
		if err != nil || information.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: linked or unreadable child %q", ErrClosure, relative)
		}
		if information.IsDir() {
			if _, expected := state.directories[relative]; !expected {
				return fmt.Errorf("%w: extra directory %q", ErrClosure, relative)
			}
			if err := walkClosure(ctx, root, relative, state); err != nil {
				return err
			}
			continue
		}
		if !information.Mode().IsRegular() {
			return fmt.Errorf("%w: special file %q", ErrClosure, relative)
		}
		if _, expected := state.files[relative]; !expected {
			return fmt.Errorf("%w: extra file %q", ErrClosure, relative)
		}
		if state.verified != nil {
			identity := state.verified[relative]
			if identity == nil || unsafeSpecialMode(information.Mode()) ||
				!secureRegularFileMode(information.Mode()) || !os.SameFile(identity, information) ||
				identity.Size() != information.Size() || identity.Mode().Perm() != information.Mode().Perm() {
				return fmt.Errorf("%w: file identity %q", ErrContent, relative)
			}
		}
		state.seen[relative] = struct{}{}
	}
	return nil
}

func addParentDirectories(set map[string]struct{}, file string) {
	parent := path.Dir(file)
	for parent != "." {
		set[parent] = struct{}{}
		parent = path.Dir(parent)
	}
}

func fileByPath(files []FileEntry, wanted string) (FileEntry, bool) {
	index := sort.Search(len(files), func(index int) bool { return files[index].Path >= wanted })
	if index < len(files) && files[index].Path == wanted {
		return files[index], true
	}
	return FileEntry{}, false
}

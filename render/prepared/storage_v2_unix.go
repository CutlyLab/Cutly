//go:build darwin || linux

package prepared

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type anchoredDirectoryV2 struct {
	parent *os.File
	name   string
	file   *os.File
}
type anchoredFileV2 struct {
	parent *os.File
	name   string
	file   *os.File
	info   os.FileInfo
}
type anchoredStorageV2 struct {
	store                                          *Store
	root, namespace, entries, scratch, locks, lock *os.File
	directories                                    []anchoredDirectoryV2
	files                                          []anchoredFileV2
	published                                      bool
}

func checkedInfoV2(f *os.File, directory, private bool) (os.FileInfo, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return nil, ErrCorrupt
	}
	if private && (stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0) {
		return nil, fmt.Errorf("%w: v2 owner or private permissions", ErrCorrupt)
	}
	if !directory && stat.Nlink != 1 {
		return nil, fmt.Errorf("%w: multiply linked v2 file", ErrCorrupt)
	}
	return info, nil
}
func openComponentV2(parent *os.File, name string, flags int, mode uint32) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return nil, ErrCorrupt
	}
	fd, err := openatV2(int(parent.Fd()), name, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func sameLinkedV2(parent *os.File, name string, file *os.File, directory bool) error {
	flags := syscall.O_RDONLY
	if directory {
		flags |= syscall.O_DIRECTORY
	}
	current, err := openComponentV2(parent, name, flags, 0)
	if err != nil {
		return fmt.Errorf("%w: linked component %s: %w", ErrCorrupt, name, err)
	}
	defer current.Close()
	a, e := checkedInfoV2(file, directory, true)
	if e != nil {
		return e
	}
	b, e := checkedInfoV2(current, directory, true)
	if e != nil {
		return e
	}
	if !os.SameFile(a, b) {
		return fmt.Errorf("%w: v2 inode substitution", ErrCorrupt)
	}
	return nil
}
func (s *Store) pinStorageIdentityV2(name string, info os.FileInfo) error {
	s.identitiesMuV2.Lock()
	defer s.identitiesMuV2.Unlock()
	old := s.identitiesV2[name]
	if old != nil {
		previous, ok1 := old.Sys().(*syscall.Stat_t)
		current, ok2 := info.Sys().(*syscall.Stat_t)
		if !os.SameFile(old, info) || !ok1 || !ok2 || previous.Uid != current.Uid || previous.Gid != current.Gid {
			return fmt.Errorf("%w: v2 namespace identity or owner changed", ErrCorrupt)
		}
	}
	if s.identitiesV2 == nil {
		return ErrConfiguration
	}
	s.identitiesV2[name] = info
	return nil
}

// openHostRootV2 walks from / one descriptor at a time. O_NOFOLLOW applies
// to every component, not only the store basename; no os.Root symlink traversal
// or pathname check/use pair is used for any v2 filesystem operation.
func (s *Store) openHostRootV2() (*os.File, error) {
	if s.rootIdentityV2 == nil || !filepath.IsAbs(s.root) || filepath.Clean(s.root) != s.root {
		return nil, ErrConfiguration
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), "/")
	path := ""
	for _, part := range strings.Split(strings.TrimPrefix(s.root, "/"), "/") {
		next, e := openComponentV2(current, part, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
		current.Close()
		if e != nil {
			return nil, errors.Join(ErrCorrupt, e)
		}
		path += "/" + part
		info, e := checkedInfoV2(next, true, path == s.root)
		if e == nil {
			e = s.pinStorageIdentityV2("host:"+path, info)
		}
		if e != nil {
			next.Close()
			return nil, e
		}
		current = next
	}
	info, err := checkedInfoV2(current, true, true)
	if err != nil || !os.SameFile(s.rootIdentityV2, info) {
		current.Close()
		return nil, errors.Join(ErrCorrupt, err)
	}
	return current, nil
}
func (x *anchoredStorageV2) directory(parent *os.File, name, pin string, create bool) (*os.File, error) {
	if create {
		if e := mkdiratV2(int(parent.Fd()), name); e != nil && !errors.Is(e, syscall.EEXIST) {
			return nil, e
		}
	}
	f, err := openComponentV2(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, fmt.Errorf("open directory %s: %w", name, err)
	}
	info, err := checkedInfoV2(f, true, true)
	if err == nil && pin != "" {
		err = x.store.pinStorageIdentityV2(pin, info)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	x.directories = append(x.directories, anchoredDirectoryV2{parent, name, f})
	if create {
		if err = parent.Sync(); err != nil {
			return nil, err
		}
	}
	return f, nil
}
func (x *anchoredStorageV2) close() {
	for _, file := range x.files {
		_ = file.file.Close()
	}
	if x.lock != nil {
		_ = syscall.Flock(int(x.lock.Fd()), syscall.LOCK_UN)
		_ = x.lock.Close()
	}
	for i := len(x.directories) - 1; i >= 0; i-- {
		_ = x.directories[i].file.Close()
	}
	if x.root != nil {
		_ = x.root.Close()
	}
}
func (x *anchoredStorageV2) check() error {
	root, err := x.store.openHostRootV2()
	if err != nil {
		return err
	}
	defer root.Close()
	a, e := root.Stat()
	if e != nil {
		return e
	}
	b, e := x.root.Stat()
	if e != nil {
		return e
	}
	if !os.SameFile(a, b) {
		return ErrCorrupt
	}
	for _, d := range x.directories {
		if e = sameLinkedV2(d.parent, d.name, d.file, true); e != nil {
			return e
		}
		if d.parent == x.entries {
			items, err := listStorageV2(d.file, 2)
			if err != nil || len(items) != 2 || !hasEntry(items, recordName) || !hasEntry(items, "complete.v2") {
				return errors.Join(ErrCorrupt, err)
			}
		}
	}
	for _, file := range x.files {
		if e = sameLinkedV2(file.parent, file.name, file.file, false); e != nil {
			return e
		}
		now, e := file.file.Stat()
		if e != nil {
			return e
		}
		if now.Size() != file.info.Size() || !now.ModTime().Equal(file.info.ModTime()) {
			return ErrCorrupt
		}
	}
	if x.lock != nil {
		return sameLinkedV2(x.locks, "global.lock", x.lock, false)
	}
	return nil
}
func (x *anchoredStorageV2) inject(point string) error {
	if x.store.fault != nil {
		return x.store.fault("v2." + point)
	}
	return nil
}

// One anchored global lock serializes both admission and publication across
// processes. No pathname-based legacy lock helper is used. Opening fresh lock
// descriptors plus flock works across goroutines without a process-local gate.
func (s *Store) withStorageV2(ctx context.Context, create bool, action func(storageSessionV2) error) (err error) {
	x := &anchoredStorageV2{store: s}
	defer x.close()
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("%w: storage panic", ErrInvalidEnvironmentV2)
		}
		if err != nil && x.published {
			err = errors.Join(ErrDurabilityUncertain, err)
		}
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	x.root, err = s.openHostRootV2()
	if err != nil {
		return err
	}
	x.namespace, err = x.directory(x.root, namespaceV2, "v2", create)
	if errors.Is(err, os.ErrNotExist) && !create {
		return ErrNotFound
	}
	if err != nil {
		return errors.Join(ErrCorrupt, err)
	}
	x.locks, err = x.directory(x.namespace, "locks", "v2/locks", create)
	if err != nil {
		return errors.Join(ErrCorrupt, err)
	}
	flags := syscall.O_RDWR
	if create {
		flags |= syscall.O_CREAT
	}
	for attempt := 0; ; attempt++ {
		x.lock, err = openComponentV2(x.locks, "global.lock", flags, 0600)
		if err == nil {
			break
		}
		// Darwin can transiently return ENOENT while another process creates
		// the same O_CREAT name. Retry only anchored, revalidated creation;
		// never retry resolution or a substituted namespace.
		if !create || !errors.Is(err, os.ErrNotExist) || attempt >= 15 {
			return fmt.Errorf("%w: open global lock: %w", ErrCorrupt, err)
		}
		if err = x.check(); err != nil {
			return err
		}
		timer := time.NewTimer(lockPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	info, e := checkedInfoV2(x.lock, false, true)
	if e != nil {
		return e
	}
	if info.Size() != 0 {
		return ErrCorrupt
	}
	if err = s.pinStorageIdentityV2("v2/locks/global.lock", info); err != nil {
		return err
	}
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		err = syscall.Flock(int(x.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		timer := time.NewTimer(lockPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err = x.check(); err != nil {
		return err
	}
	if create {
		if err = x.locks.Sync(); err != nil {
			return err
		}
	}
	x.entries, err = x.directory(x.namespace, "entries", "v2/entries", create)
	if err != nil {
		return errors.Join(ErrCorrupt, err)
	}
	x.scratch, err = x.directory(x.namespace, "scratch", "v2/scratch", create)
	if err != nil {
		return errors.Join(ErrCorrupt, err)
	}
	if err = x.inject("locked"); err != nil {
		return err
	}
	if err = x.check(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = action(x); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return x.check()
}

func listStorageV2(directory *os.File, maximum int) ([]os.DirEntry, error) {
	// Open a fresh description: dup would share the directory read offset.
	fd, err := openatV2(int(directory.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), ".")
	defer f.Close()
	out, err := f.ReadDir(maximum + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(out) > maximum {
		return nil, ErrLimit
	}
	return out, nil
}
func (x *anchoredStorageV2) readFile(ctx context.Context, dir *os.File, name string, maximum int) ([]byte, error) {
	f, err := openComponentV2(dir, name, syscall.O_RDONLY, 0)
	if err != nil {
		return nil, errors.Join(ErrCorrupt, err)
	}
	retained := false
	defer func() {
		if !retained {
			_ = f.Close()
		}
	}()
	before, err := checkedInfoV2(f, false, true)
	if err != nil {
		return nil, err
	}
	if before.Size() < 0 || before.Size() > int64(maximum) {
		return nil, ErrLimit
	}
	raw := make([]byte, 0, min(int(before.Size()), 32<<10))
	buffer := make([]byte, 64<<10)
	for {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		n, e := f.Read(buffer)
		if len(raw)+n > maximum {
			return nil, ErrLimit
		}
		raw = append(raw, buffer[:n]...)
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if n == 0 {
			return nil, io.ErrNoProgress
		}
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, ErrCorrupt
	}
	if err = sameLinkedV2(dir, name, f, false); err != nil {
		return nil, err
	}
	x.files = append(x.files, anchoredFileV2{dir, name, f, before})
	retained = true
	return raw, nil
}
func (x *anchoredStorageV2) read(ctx context.Context, name string) ([]byte, error) {
	dir, err := openComponentV2(x.entries, name, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, errors.Join(ErrCorrupt, err)
	}
	x.directories = append(x.directories, anchoredDirectoryV2{x.entries, name, dir})
	if _, err = checkedInfoV2(dir, true, true); err != nil {
		return nil, err
	}
	items, err := listStorageV2(dir, 2)
	if err != nil {
		return nil, errors.Join(ErrCorrupt, err)
	}
	if len(items) != 2 || !hasEntry(items, recordName) || !hasEntry(items, "complete.v2") {
		return nil, ErrCorrupt
	}
	raw, err := x.readFile(ctx, dir, recordName, x.store.maxRecordBytesV2)
	if err != nil {
		return nil, err
	}
	marker, err := x.readFile(ctx, dir, "complete.v2", 256)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(marker, completionBytesV2(raw)) {
		return nil, ErrCorrupt
	}
	if err = sameLinkedV2(x.entries, name, dir, true); err != nil {
		return nil, err
	}
	if err = x.check(); err != nil {
		return nil, err
	}
	return raw, nil
}
func (x *anchoredStorageV2) writeFile(ctx context.Context, dir *os.File, name string, raw []byte) (err error) {
	f, err := openComponentV2(dir, name, syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY, 0400)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	for offset := 0; offset < len(raw); {
		if err = ctx.Err(); err != nil {
			return err
		}
		n, e := f.Write(raw[offset:min(offset+64<<10, len(raw))])
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		offset += n
	}
	if err = x.inject("file.before_sync:" + name); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	return sameLinkedV2(dir, name, f, false)
}
func (x *anchoredStorageV2) sync(name string) error {
	// This also covers idempotent publication: any later error or panic is
	// about an already-visible entry, not a promise that nothing was written.
	x.published = true
	if err := x.inject("publish.before_sync"); err != nil {
		return errors.Join(ErrDurabilityUncertain, err)
	}
	dir, err := openComponentV2(x.entries, name, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return errors.Join(ErrDurabilityUncertain, err)
	}
	defer dir.Close()
	if err = x.check(); err != nil {
		return errors.Join(ErrDurabilityUncertain, err)
	}
	// Sync the exact record and marker descriptors retained by read(), not
	// merely the staging inodes written earlier. A same-byte replacement may
	// have occurred before that verified read, or this may be an idempotent
	// retry recovering an earlier uncertain publication. Directory fsync alone
	// cannot make those current file contents durable.
	if len(x.files) != 2 {
		return errors.Join(ErrDurabilityUncertain, ErrCorrupt)
	}
	for _, file := range x.files {
		if err = x.inject("committed.file.before_sync:" + file.name); err != nil {
			return errors.Join(ErrDurabilityUncertain, err)
		}
		if err = file.file.Sync(); err != nil {
			return errors.Join(ErrDurabilityUncertain, err)
		}
		if err = x.inject("committed.file.after_sync:" + file.name); err != nil {
			return errors.Join(ErrDurabilityUncertain, err)
		}
	}
	// Validate retained identities again before syncing the directory chain;
	// substitution during file sync must not yield a durable-success result.
	if err = x.check(); err != nil {
		return errors.Join(ErrDurabilityUncertain, err)
	}
	err = errors.Join(dir.Sync(), x.entries.Sync(), x.scratch.Sync(), x.namespace.Sync(), x.root.Sync(), x.check())
	if err != nil {
		return errors.Join(ErrDurabilityUncertain, err)
	}
	if err = x.inject("publish.after_sync"); err != nil {
		return errors.Join(ErrDurabilityUncertain, err)
	}
	return nil
}
func (x *anchoredStorageV2) publish(ctx context.Context, name string, raw []byte) (err error) {
	entries, err := listStorageV2(x.entries, x.store.maxEntries)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() || !validHex(entry.Name(), 64) {
			return ErrCorrupt
		}
		f, e := openComponentV2(x.entries, entry.Name(), syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
		if e != nil {
			return errors.Join(ErrCorrupt, e)
		}
		_, e = checkedInfoV2(f, true, true)
		f.Close()
		if e != nil {
			return e
		}
	}
	if len(entries) >= x.store.maxEntries {
		return ErrLimit
	}
	scratch, err := listStorageV2(x.scratch, x.store.maxScratchEntries)
	if err != nil {
		return err
	}
	if len(scratch) >= x.store.maxScratchEntries {
		return ErrLimit
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return err
	}
	tmp := ".partial-" + hex.EncodeToString(token[:])
	if err = mkdiratV2(int(x.scratch.Fd()), tmp); err != nil {
		return err
	}
	dir, err := openComponentV2(x.scratch, tmp, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	owned := true
	defer func() {
		// Cleanup only through the owned descriptor and only unlink the staging
		// directory if its current linked identity still matches our descriptor.
		if owned {
			e := sameLinkedV2(x.scratch, tmp, dir, true)
			if e == nil {
				for _, n := range []string{recordName, "complete.v2"} {
					u := unlinkatV2(int(dir.Fd()), n, false)
					if u != nil && !errors.Is(u, os.ErrNotExist) {
						e = errors.Join(e, u)
					}
				}
				if e == nil {
					e = unlinkatV2(int(x.scratch.Fd()), tmp, true)
				}
			}
			err = errors.Join(err, e, x.scratch.Sync())
		}
		err = errors.Join(err, dir.Close())
	}()
	if _, err = checkedInfoV2(dir, true, true); err != nil {
		return err
	}
	if err = x.inject("publish.before_write"); err != nil {
		return err
	}
	if err = x.writeFile(ctx, dir, recordName, raw); err != nil {
		return err
	}
	if err = x.writeFile(ctx, dir, "complete.v2", completionBytesV2(raw)); err != nil {
		return err
	}
	if err = errors.Join(dir.Sync(), x.scratch.Sync()); err != nil {
		return err
	}
	if err = x.inject("publish.before_rename"); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = x.check(); err != nil {
		return err
	}
	if err = sameLinkedV2(x.scratch, tmp, dir, true); err != nil {
		return err
	}
	if err = renameatV2(int(x.scratch.Fd()), tmp, int(x.entries.Fd()), name); err != nil {
		if errors.Is(err, syscall.EEXIST) || errors.Is(err, syscall.ENOTEMPTY) {
			return errors.Join(ErrCollision, err)
		}
		return err
	}
	owned = false
	x.published = true
	if err = x.inject("publish.after_rename"); err != nil {
		return err
	}
	if err = sameLinkedV2(x.entries, name, dir, true); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Reopen through the committed descriptor namespace and retain the entry
	// and file identities until the final post-sync validation. This catches
	// staging/record substitution at the commit hook as well as namespace moves.
	committed, e := x.read(ctx, name)
	if e != nil {
		return e
	}
	if !bytes.Equal(committed, raw) {
		return ErrCollision
	}
	return x.sync(name)
}

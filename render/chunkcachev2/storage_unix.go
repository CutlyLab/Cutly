//go:build darwin || linux

package chunkcachev2

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
)

// All pathname resolution uses descriptor-relative O_NOFOLLOW, including ancestors.
// The persistent flock serializes index generations across processes. CAS reader
// liveness is deliberately NOT represented by this lock; it uses cache/cas leases.
type session struct {
	s                                           *Store
	root, entries, scratch, quarantineDir, lock *os.File
	published                                   bool
	verifiedFile                                *os.File
	verifiedRaw                                 []byte
	verifiedName                                string
	heartbeat                                   func() error
}

func checked(f *os.File, directory, private bool) (os.FileInfo, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return nil, ErrCorrupt
	}
	if private && (st.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0) {
		return nil, ErrCorrupt
	}
	if !directory && st.Nlink != 1 {
		return nil, ErrCorrupt
	}
	return info, nil
}
func component(parent *os.File, name string, flags int, mode uint32) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return nil, ErrCorrupt
	}
	fd, err := openat(int(parent.Fd()), name, flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func openHost(path string) (*os.File, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, ErrInvalid
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		next, e := component(current, part, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
		current.Close()
		if e != nil {
			return nil, errors.Join(ErrCorrupt, e)
		}
		info, e := checked(next, true, i == len(parts)-1)
		if e == nil && i < len(parts)-1 && info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
			e = ErrCorrupt
		}
		if e != nil {
			next.Close()
			return nil, e
		}
		current = next
	}
	return current, nil
}
func same(parent *os.File, name string, f *os.File, directory bool) error {
	flags := syscall.O_RDONLY
	if directory {
		flags |= syscall.O_DIRECTORY
	}
	other, e := component(parent, name, flags, 0)
	if e != nil {
		return errors.Join(ErrCorrupt, e)
	}
	defer other.Close()
	a, e := checked(f, directory, true)
	if e != nil {
		return e
	}
	b, e := checked(other, directory, true)
	if e != nil || !os.SameFile(a, b) {
		return errors.Join(ErrCorrupt, e)
	}
	return nil
}
func (s *Store) initialize() error {
	root, e := openHost(s.root)
	if e != nil {
		return e
	}
	defer root.Close()
	s.rootInfo, e = checked(root, true, true)
	if e != nil {
		return e
	}
	s.storageInfo = make(map[string]os.FileInfo)
	for _, n := range []string{"entries", "scratch", "quarantine"} {
		if e = mkdirat(int(root.Fd()), n); e != nil && !errors.Is(e, os.ErrExist) {
			return e
		}
		f, e := component(root, n, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
		if e != nil {
			return errors.Join(ErrCorrupt, e)
		}
		s.storageInfo[n], e = checked(f, true, true)
		f.Close()
		if e != nil {
			return e
		}
	}
	lock, e := component(root, "index.lock", syscall.O_CREAT|syscall.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	info, e := checked(lock, false, true)
	if e != nil || info.Size() != 0 {
		return errors.Join(ErrCorrupt, e)
	}
	s.storageInfo["index.lock"] = info
	if e = lock.Sync(); e != nil {
		return e
	}
	return root.Sync()
}
func (x *session) setHeartbeat(fn func() error) { x.heartbeat = fn }
func (x *session) close() {
	if x.verifiedFile != nil {
		_ = x.verifiedFile.Close()
	}
	if x.lock != nil {
		_ = syscall.Flock(int(x.lock.Fd()), syscall.LOCK_UN)
		_ = x.lock.Close()
	}
	for _, f := range []*os.File{x.quarantineDir, x.scratch, x.entries, x.root} {
		if f != nil {
			_ = f.Close()
		}
	}
}
func (x *session) check() error {
	if x.heartbeat != nil {
		if err := x.heartbeat(); err != nil {
			return err
		}
	}
	root, e := openHost(x.s.root)
	if e != nil {
		return e
	}
	defer root.Close()
	info, e := root.Stat()
	if e != nil || !os.SameFile(info, x.s.rootInfo) {
		return errors.Join(ErrCorrupt, e)
	}
	for _, d := range []struct {
		name string
		f    *os.File
	}{{"entries", x.entries}, {"scratch", x.scratch}, {"quarantine", x.quarantineDir}} {
		if d.f != nil {
			info, err := d.f.Stat()
			if err != nil || !os.SameFile(info, x.s.storageInfo[d.name]) {
				return errors.Join(ErrCorrupt, err)
			}
			if e = same(x.root, d.name, d.f, true); e != nil {
				return e
			}
		}
	}
	if x.verifiedFile != nil {
		if e = x.verifyFile(x.verifiedFile, x.verifiedName, x.verifiedRaw); e != nil {
			return e
		}
	}
	if x.lock != nil {
		info, err := x.lock.Stat()
		if err != nil || !os.SameFile(info, x.s.storageInfo["index.lock"]) {
			return errors.Join(ErrCorrupt, err)
		}
		return same(x.root, "index.lock", x.lock, false)
	}
	return nil
}
func (s *Store) withSession(ctx context.Context, action func(*session) error) (err error) {
	x := &session{s: s}
	defer x.close()
	defer func() {
		if recover() != nil {
			err = ErrInvalid
		}
		if err != nil && x.published {
			err = errors.Join(ErrDurabilityUncertain, err)
		}
	}()
	if err = checkContext(ctx); err != nil {
		return err
	}
	x.root, err = openHost(s.root)
	if err != nil {
		return err
	}
	info, err := x.root.Stat()
	if err != nil || !os.SameFile(info, s.rootInfo) {
		return errors.Join(ErrCorrupt, err)
	}
	x.lock, err = component(x.root, "index.lock", syscall.O_RDWR, 0)
	if err != nil {
		return errors.Join(ErrCorrupt, err)
	}
	info, err = checked(x.lock, false, true)
	if err != nil || info.Size() != 0 {
		return errors.Join(ErrCorrupt, err)
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
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	for _, d := range []struct {
		name string
		dest **os.File
	}{{"entries", &x.entries}, {"scratch", &x.scratch}, {"quarantine", &x.quarantineDir}} {
		*d.dest, err = component(x.root, d.name, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
		if err != nil {
			return errors.Join(ErrCorrupt, err)
		}
		if _, err = checked(*d.dest, true, true); err != nil {
			return err
		}
	}
	if err = x.check(); err != nil {
		return err
	}
	if err = s.inject("locked"); err != nil {
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
func (x *session) read(k cachekey.Key) ([]byte, error) {
	name := entryName(k)
	f, e := component(x.entries, name, syscall.O_RDONLY, 0)
	if e != nil {
		if errors.Is(e, os.ErrNotExist) {
			return nil, e
		}
		return nil, errors.Join(ErrCorrupt, e)
	}
	retained := false
	defer func() {
		if !retained {
			_ = f.Close()
		}
	}()
	before, e := checked(f, false, true)
	if e != nil {
		return nil, e
	}
	if before.Size() <= 0 || before.Size() > MaxRecordBytes {
		return nil, ErrLimit
	}
	raw, e := io.ReadAll(io.LimitReader(f, MaxRecordBytes+1))
	if e != nil {
		return nil, e
	}
	after, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if len(raw) > MaxRecordBytes || int64(len(raw)) != before.Size() || after.Size() != before.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, ErrCorrupt
	}
	if e = same(x.entries, name, f, false); e != nil {
		return nil, e
	}
	if x.verifiedFile != nil {
		_ = x.verifiedFile.Close()
	}
	x.verifiedFile = f
	x.verifiedRaw = raw
	x.verifiedName = name
	retained = true
	return raw, nil
}
func list(dir *os.File, maximum int) ([]os.DirEntry, error) {
	fd, e := openat(int(dir.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), ".")
	defer f.Close()
	items, e := f.ReadDir(maximum + 1)
	if e != nil && !errors.Is(e, io.EOF) {
		return nil, e
	}
	if len(items) > maximum {
		return nil, ErrLimit
	}
	return items, nil
}
func (x *session) quarantine(k cachekey.Key) error {
	items, e := list(x.quarantineDir, x.s.maxEntries)
	if e != nil {
		return e
	}
	if len(items) >= x.s.maxEntries {
		return ErrLimit
	}
	n, e := nonce()
	if e != nil {
		return e
	}
	name := entryName(k)
	target := name + "-" + n
	// No-follow rename quarantines only this index generation, never a CAS payload.
	if e = renameExclusive(int(x.entries.Fd()), name, int(x.quarantineDir.Fd()), target); e != nil {
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		return e
	}
	if x.verifiedFile != nil && x.verifiedName == name {
		_ = x.verifiedFile.Close()
		x.verifiedFile = nil
		x.verifiedRaw = nil
		x.verifiedName = ""
	}
	return errors.Join(x.entries.Sync(), x.quarantineDir.Sync())
}
func (x *session) verifyFile(f *os.File, name string, raw []byte) error {
	if e := same(x.entries, name, f, false); e != nil {
		return e
	}
	info, e := checked(f, false, true)
	if e != nil || info.Size() != int64(len(raw)) {
		return errors.Join(ErrCorrupt, e)
	}
	actual := make([]byte, len(raw))
	n, e := f.ReadAt(actual, 0)
	if e != nil || n != len(raw) || !bytes.Equal(actual, raw) {
		return errors.Join(ErrCorrupt, e)
	}
	return same(x.entries, name, f, false)
}
func (x *session) resync(k cachekey.Key) error {
	x.published = true
	if x.verifiedFile == nil || x.verifiedName != entryName(k) {
		return ErrCorrupt
	}
	if e := x.verifyFile(x.verifiedFile, x.verifiedName, x.verifiedRaw); e != nil {
		return e
	}
	if e := errors.Join(x.verifiedFile.Sync(), x.entries.Sync(), x.root.Sync()); e != nil {
		return e
	}
	return x.check()
}
func (x *session) admitPublication(k cachekey.Key) error {
	items, e := list(x.entries, x.s.maxEntries)
	if e != nil {
		return e
	}
	found := false
	for _, item := range items {
		if item.Name() == entryName(k) {
			found = true
		}
	}
	if !found && len(items) >= x.s.maxEntries {
		return ErrLimit
	}
	if _, e = list(x.scratch, x.s.maxEntries); e != nil {
		return e
	}
	if _, e = list(x.quarantineDir, x.s.maxEntries); e != nil {
		return e
	}
	return nil
}
func (x *session) publish(ctx context.Context, k cachekey.Key, raw []byte) (err error) {
	// All scratch is protected by the index flock, so leftovers cannot belong to an
	// active publisher. Remove only bounded, private regular scratch files. Never
	// recursively remove unexpected directories or a shared CAS/object namespace.
	items, err := list(x.scratch, x.s.maxEntries)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err = ctx.Err(); err != nil {
			return err
		}
		if !strings.HasPrefix(item.Name(), "partial-") {
			return ErrCorrupt
		}
		f, e := component(x.scratch, item.Name(), syscall.O_RDONLY, 0)
		if e != nil {
			return errors.Join(ErrCorrupt, e)
		}
		_, e = checked(f, false, true)
		if e != nil {
			f.Close()
			return e
		}
		e = same(x.scratch, item.Name(), f, false)
		f.Close()
		if e != nil {
			return e
		}
		if e = unlinkat(int(x.scratch.Fd()), item.Name()); e != nil {
			return e
		}
	}
	if err = x.scratch.Sync(); err != nil {
		return err
	}
	items, err = list(x.entries, x.s.maxEntries)
	if err != nil {
		return err
	}
	if len(items) >= x.s.maxEntries {
		return ErrLimit
	}
	token, err := nonce()
	if err != nil {
		return err
	}
	name := "partial-" + token
	f, err := component(x.scratch, name, syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR, 0600)
	if err != nil {
		return err
	}
	owned := true
	defer func() {
		if owned {
			e := same(x.scratch, name, f, false)
			if e == nil {
				e = unlinkat(int(x.scratch.Fd()), name)
			}
			// A foreign replacement is not ours to remove, even on failure.
			err = errors.Join(err, e, x.scratch.Sync())
		}
		if x.verifiedFile != f {
			err = errors.Join(err, f.Close())
		}
	}()
	if err = x.s.inject("before-write"); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = same(x.scratch, name, f, false); err != nil {
		return err
	}
	if err = x.check(); err != nil {
		return err
	}
	if err = x.s.inject("before-commit"); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = same(x.scratch, name, f, false); err != nil {
		return err
	}
	if err = x.check(); err != nil {
		return err
	}
	// Kernel no-replace rename commits a complete fsynced file atomically. The
	// persistent flock also serializes quarantine and crash scratch recovery.
	if err = renameExclusive(int(x.scratch.Fd()), name, int(x.entries.Fd()), entryName(k)); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrCollision
		}
		return err
	}
	owned = false
	x.published = true
	if x.verifiedFile != nil {
		_ = x.verifiedFile.Close()
	}
	x.verifiedFile = f
	x.verifiedRaw = raw
	x.verifiedName = entryName(k)
	if err = x.s.inject("after-commit"); err != nil {
		return err
	}
	if err = x.resync(k); err != nil {
		return err
	}
	if err = x.s.inject("after-sync"); err != nil {
		return err
	}
	return x.check()
}

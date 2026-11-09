package releasearchive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path"
	"runtime/debug"
	"sort"
	"strings"
)

func names(p plan) []string {
	result := make([]string, 0, len(p.dirs)+len(p.files))
	for name := range p.dirs {
		result = append(result, name+"/")
	}
	for name := range p.files {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func writeArchive(ctx context.Context, file string, p plan) (err error) {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { err = joinClose(err, f.Close()) }()
	if p.target.os == "windows" {
		w := zip.NewWriter(f)
		for _, name := range names(p) {
			if err := ctx.Err(); err != nil {
				_ = w.Close()
				return err
			}
			h := &zip.FileHeader{Name: p.root + "/" + name, Method: zip.Deflate}
			if name == "/" {
				h.Name = p.root + "/"
			}
			h.SetMode(0755 | os.ModeDir)
			if m, ok := p.files[name]; ok {
				h.SetMode(m.mode)
			}
			dst, err := w.CreateHeader(h)
			if err != nil {
				_ = w.Close()
				return err
			}
			if m, ok := p.files[name]; ok {
				if err := copySource(ctx, dst, m); err != nil {
					_ = w.Close()
					return err
				}
			}
		}
		return w.Close()
	}
	gz := gzip.NewWriter(f)
	w := tar.NewWriter(gz)
	for _, name := range names(p) {
		if err := ctx.Err(); err != nil {
			_ = w.Close()
			_ = gz.Close()
			return err
		}
		h := &tar.Header{Name: p.root + "/" + name, Mode: 0755, Typeflag: tar.TypeDir}
		if name == "/" {
			h.Name = p.root + "/"
		}
		if m, ok := p.files[name]; ok {
			h.Mode = int64(m.mode)
			h.Typeflag = tar.TypeReg
			h.Size = m.size
		}
		if err := w.WriteHeader(h); err != nil {
			_ = w.Close()
			_ = gz.Close()
			return err
		}
		if m, ok := p.files[name]; ok {
			if err := copySource(ctx, w, m); err != nil {
				_ = w.Close()
				_ = gz.Close()
				return err
			}
		}
	}
	err = w.Close()
	return joinClose(err, gz.Close())
}

func joinClose(first, second error) error {
	if first != nil {
		return first
	}
	return second
}

func copySource(ctx context.Context, dst io.Writer, m member) error {
	f, err := os.Open(m.source)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), &contextReader{ctx: ctx, r: io.LimitReader(f, maxFileBytes+1)})
	if err != nil {
		return err
	}
	if n != m.size || !bytes.Equal(h.Sum(nil), m.digest[:]) {
		return fmt.Errorf("source changed while archiving: %s", m.source)
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func validate(ctx context.Context, file string, p plan, readInfo func(io.ReaderAt) (*debug.BuildInfo, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(file)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("archive is not a regular file")
	}
	seen := map[string]bool{}
	check := func(name string, mode os.FileMode, size int64, r io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// No extraction is performed. Still enforce portable paths, one root, and
		// only regular files/directories before consuming any member contents.
		trimmed := strings.TrimSuffix(name, "/")
		if strings.ContainsAny(name, "\\\x00:") || strings.HasPrefix(name, "/") || path.Clean(trimmed) != trimmed {
			return fmt.Errorf("unsafe member path %q", name)
		}
		if seen[trimmed] {
			return fmt.Errorf("duplicate member %q", name)
		}
		seen[trimmed] = true
		rel := ""
		if trimmed != p.root {
			if !strings.HasPrefix(trimmed, p.root+"/") {
				return fmt.Errorf("member outside sole root: %s", name)
			}
			rel = strings.TrimPrefix(trimmed, p.root+"/")
		}
		if p.dirs[rel] {
			if !mode.IsDir() || !strings.HasSuffix(name, "/") || size != 0 {
				return fmt.Errorf("invalid directory member %s", name)
			}
			if mode.Perm() != 0755 {
				return fmt.Errorf("invalid directory mode %s", name)
			}
			return nil
		}
		m, ok := p.files[rel]
		if !ok {
			return fmt.Errorf("unexpected member %s", name)
		}
		if !mode.IsRegular() || strings.HasSuffix(name, "/") {
			return fmt.Errorf("member is not regular: %s", name)
		}
		if mode.Perm() != m.mode.Perm() {
			return fmt.Errorf("invalid member mode: %s", name)
		}
		if size < 0 || size > maxFileBytes || (m.size >= 0 && size != m.size) {
			return fmt.Errorf("invalid member size: %s", name)
		}
		command := strings.TrimSuffix(rel, ".exe")
		binary := false
		for _, candidate := range commandNames(p.target) {
			if command == candidate {
				binary = true
				break
			}
		}
		h := sha256.New()
		var data bytes.Buffer
		dst := io.Writer(h)
		if binary {
			dst = io.MultiWriter(h, &data)
		}
		n, err := io.Copy(dst, &contextReader{ctx: ctx, r: io.LimitReader(r, size+1)})
		if err != nil {
			return err
		}
		if n != size {
			return fmt.Errorf("member size mismatch: %s", name)
		}
		if m.size >= 0 && !bytes.Equal(h.Sum(nil), m.digest[:]) {
			return fmt.Errorf("member differs from source: %s", name)
		}
		if binary {
			bi, err := readInfo(bytes.NewReader(data.Bytes()))
			if err != nil {
				return fmt.Errorf("binary build information %s: %w", name, err)
			}
			if err := validateBuildInfo(bi, p.target, command); err != nil {
				return err
			}
		}
		return nil
	}
	if p.target.os == "windows" {
		z, err := zip.OpenReader(file)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			if f.UncompressedSize64 > maxFileBytes {
				return fmt.Errorf("zip member too large")
			}
			r, err := f.Open()
			if err != nil {
				return err
			}
			err = check(f.Name, f.Mode(), int64(f.UncompressedSize64), r)
			err = joinClose(err, r.Close())
			if err != nil {
				return err
			}
		}
	} else {
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
				return fmt.Errorf("unsupported tar member type: %s", h.Name)
			}
			if h.Linkname != "" {
				return fmt.Errorf("tar link rejected")
			}
			if err := check(h.Name, h.FileInfo().Mode(), h.Size, tr); err != nil {
				return err
			}
		}
		// Read through gzip EOF to verify its checksum and reject extra payloads.
		extra := make([]byte, 1)
		n, err := gz.Read(extra)
		if n != 0 || err != io.EOF {
			return fmt.Errorf("trailing or corrupt gzip payload: %v", err)
		}
	}
	if len(seen) != len(p.dirs)+len(p.files) {
		return fmt.Errorf("archive member set incomplete: got %d want %d", len(seen), len(p.dirs)+len(p.files))
	}
	return nil
}

func validateBuildInfo(bi *debug.BuildInfo, t target, name string) error {
	if bi == nil || bi.Path != modulePath+"/cmd/"+name || bi.Main.Path != modulePath {
		return fmt.Errorf("unexpected %s binary import/module path", name)
	}
	expected := map[string]string{"GOOS": t.os, "GOARCH": t.arch, "CGO_ENABLED": "0", "-trimpath": "true"}
	seen := map[string]bool{}
	for _, s := range bi.Settings {
		if want, ok := expected[s.Key]; ok {
			if seen[s.Key] || s.Value != want {
				return fmt.Errorf("%s build setting %s=%q, want %q", name, s.Key, s.Value, want)
			}
			seen[s.Key] = true
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("missing %s binary target build settings", name)
	}
	return nil
}

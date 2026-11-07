package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

const (
	renderStateProfileName    = ".cutlyd-render-state.json"
	renderStatePendingName    = ".cutlyd-render-state.pending"
	renderStateProfileV1      = "cutly.daemon-render-state.v1"
	maxRenderStateProfileSize = 32 << 10
)

var ErrRenderStateProjectMismatch = errors.New("cutlyd: render state belongs to another project")
var ErrRenderStateUnboundData = errors.New("cutlyd: unbound render state contains durable data")

type renderStateProfile struct {
	Profile     string `json:"profile"`
	ProjectID   string `json:"project_id"`
	ProjectRoot string `json:"project_root"`
	OutputRoot  string `json:"output_root"`
}

// bindRenderStateProject permanently dedicates a durable scheduler namespace
// to one project identity and resolved project root. The render-state owner
// lock must already be held by the caller. Exact canonical bytes make a
// corrupt, downgraded, or partially written marker fail closed on restart.
func bindRenderStateProject(ctx context.Context, stateDir, projectID, projectDir, outputDir string) error {
	if ctx == nil {
		return errors.New("cutlyd: render state profile context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{
		{"state", stateDir}, {"project", projectDir}, {"output", outputDir},
	} {
		if err := validateCleanAbsolutePath(field.value); err != nil {
			return fmt.Errorf("cutlyd: render state %s root: %w", field.name, err)
		}
	}
	projectRoot, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		return fmt.Errorf("cutlyd: resolve project root for render state: %w", err)
	}
	projectRoot = filepath.Clean(projectRoot)
	information, err := os.Lstat(projectRoot)
	if err != nil || !information.IsDir() || information.Mode()&os.ModeSymlink != 0 {
		return errors.Join(errors.New("cutlyd: resolved project root must be a real directory"), err)
	}
	outputRoot, err := filepath.EvalSymlinks(outputDir)
	if err != nil {
		return fmt.Errorf("cutlyd: resolve output root for render state: %w", err)
	}
	outputRoot = filepath.Clean(outputRoot)
	information, err = os.Lstat(outputRoot)
	if err != nil || !information.IsDir() || information.Mode()&os.ModeSymlink != 0 {
		return errors.Join(errors.New("cutlyd: resolved output root must be a real directory"), err)
	}
	wanted, err := videoedit.CanonicalJSON(renderStateProfile{
		Profile: renderStateProfileV1, ProjectID: projectID, ProjectRoot: projectRoot, OutputRoot: outputRoot,
	})
	if err != nil {
		return fmt.Errorf("cutlyd: encode render state profile: %w", err)
	}
	if len(wanted) > maxRenderStateProfileSize {
		return errors.New("cutlyd: render state profile exceeds its fixed size limit")
	}
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		return fmt.Errorf("cutlyd: open render state root: %w", err)
	}
	defer root.Close()

	if err := verifyRenderStateProfile(ctx, root, wanted); err == nil {
		return cleanupRenderStatePending(root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := requireEmptyUnboundRenderState(ctx, root); err != nil {
		return err
	}
	if err := cleanupRenderStatePending(root); err != nil {
		return err
	}
	file, err := root.OpenFile(renderStatePendingName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("cutlyd: create pending render state profile: %w", err)
	}
	if err := writeAll(file, wanted); err != nil {
		return abortRenderStateProfile(root, file, fmt.Errorf("cutlyd: write render state profile: %w", err))
	}
	if err := file.Sync(); err != nil {
		return abortRenderStateProfile(root, file, fmt.Errorf("cutlyd: sync render state profile: %w", err))
	}
	opened, openedErr := file.Stat()
	named, namedErr := root.Lstat(renderStatePendingName)
	if openedErr != nil || namedErr != nil || !opened.Mode().IsRegular() || named.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, named) {
		return abortRenderStateProfile(root, file,
			errors.Join(errors.New("cutlyd: pending render state profile path identity changed"), openedErr, namedErr))
	}
	if err := file.Close(); err != nil {
		return errors.Join(fmt.Errorf("cutlyd: close pending render state profile: %w", err), cleanupRenderStatePending(root))
	}
	if err := root.Link(renderStatePendingName, renderStateProfileName); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.Join(verifyRenderStateProfile(ctx, root, wanted), cleanupRenderStatePending(root))
		}
		return errors.Join(fmt.Errorf("cutlyd: publish render state profile: %w", err), cleanupRenderStatePending(root))
	}
	pending, pendingErr := root.Lstat(renderStatePendingName)
	published, publishedErr := root.Lstat(renderStateProfileName)
	if pendingErr != nil || publishedErr != nil || !os.SameFile(pending, published) {
		return errors.Join(errors.New("cutlyd: published render state profile identity changed"), pendingErr, publishedErr)
	}
	if err := syncRenderStateRoot(root); err != nil {
		return fmt.Errorf("cutlyd: sync render state root: %w", err)
	}
	return cleanupRenderStatePending(root)
}

func requireEmptyUnboundRenderState(ctx context.Context, root *os.Root) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("cutlyd: inspect unbound render state: %w", err)
	}
	entries, readErr := directory.ReadDir(33)
	closeErr := directory.Close()
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, closeErr, ctx.Err()); err != nil {
		return fmt.Errorf("cutlyd: inspect unbound render state: %w", err)
	}
	if len(entries) > 32 {
		return ErrRenderStateUnboundData
	}
	for _, entry := range entries {
		switch entry.Name() {
		case renderStateOwnerLockName, renderStatePendingName:
			continue
		default:
			return ErrRenderStateUnboundData
		}
	}
	return nil
}

func verifyRenderStateProfile(ctx context.Context, root *os.Root, wanted []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	named, err := root.Lstat(renderStateProfileName)
	if err != nil {
		return err
	}
	if !named.Mode().IsRegular() || named.Mode()&os.ModeSymlink != 0 || named.Size() < 1 || named.Size() > maxRenderStateProfileSize {
		return ErrRenderStateProjectMismatch
	}
	file, err := root.Open(renderStateProfileName)
	if err != nil {
		return fmt.Errorf("cutlyd: open render state profile: %w", err)
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(opened, named) {
		_ = file.Close()
		return errors.Join(ErrRenderStateProjectMismatch, statErr)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxRenderStateProfileSize+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr, ctx.Err()); err != nil {
		return fmt.Errorf("cutlyd: read render state profile: %w", err)
	}
	if !bytes.Equal(raw, wanted) {
		return ErrRenderStateProjectMismatch
	}
	return nil
}

func writeAll(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		written, err := writer.Write(value)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(value) {
			return io.ErrShortWrite
		}
		value = value[written:]
	}
	return nil
}

func abortRenderStateProfile(root *os.Root, file *os.File, primary error) error {
	return errors.Join(primary, file.Close(), cleanupRenderStatePending(root))
}

func cleanupRenderStatePending(root *os.Root) error {
	information, err := root.Lstat(renderStatePendingName)
	if errors.Is(err, os.ErrNotExist) {
		return syncRenderStateRoot(root)
	}
	if err != nil {
		return fmt.Errorf("cutlyd: inspect pending render state profile: %w", err)
	}
	if !information.Mode().IsRegular() || information.Mode()&os.ModeSymlink != 0 {
		return ErrRenderStateProjectMismatch
	}
	if err := root.Remove(renderStatePendingName); err != nil {
		return fmt.Errorf("cutlyd: remove pending render state profile: %w", err)
	}
	return syncRenderStateRoot(root)
}

func syncRenderStateRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

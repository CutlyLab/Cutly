package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRenderStateProfilePermanentlyBindsProjectNamespace(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	firstProject := filepath.Join(root, "first")
	secondProject := filepath.Join(root, "second")
	firstOutput := filepath.Join(root, "first-output")
	secondOutput := filepath.Join(root, "second-output")
	for _, directory := range []string{state, firstProject, secondProject, firstOutput, secondOutput} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := bindRenderStateProject(context.Background(), state, "project-one", firstProject, firstOutput); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(state, renderStatePendingName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending profile remains: %v", err)
	}
	if err := bindRenderStateProject(context.Background(), state, "project-one", firstProject, firstOutput); err != nil {
		t.Fatalf("same project replay: %v", err)
	}
	for _, test := range []struct {
		name, projectID, projectRoot, outputRoot string
	}{
		{"different identity", "project-two", firstProject, firstOutput},
		{"different root", "project-one", secondProject, firstOutput},
		{"different output", "project-one", firstProject, secondOutput},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := bindRenderStateProject(context.Background(), state, test.projectID, test.projectRoot, test.outputRoot); !errors.Is(err, ErrRenderStateProjectMismatch) {
				t.Fatalf("mismatch error = %v", err)
			}
		})
	}
}

func TestRenderStateProfileRecoversUnpublishedPendingFile(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	project := filepath.Join(root, "project")
	output := filepath.Join(root, "output")
	for _, directory := range []string{state, project, output} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(state, renderStatePendingName), []byte("crashed writer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := bindRenderStateProject(context.Background(), state, "project", project, output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(state, renderStatePendingName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending profile remains: %v", err)
	}
}

func TestRenderStateProfileRejectsUnmarkedDurableData(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	output := filepath.Join(root, "output")
	for _, directory := range []string{project, output} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"scheduler.journal", "prepared"} {
		t.Run(name, func(t *testing.T) {
			state := filepath.Join(root, name+"-state")
			if err := os.Mkdir(state, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(state, name)
			if name == "prepared" {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("old durable state"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := bindRenderStateProject(context.Background(), state, "project", project, output); !errors.Is(err, ErrRenderStateUnboundData) {
				t.Fatalf("unmarked state error = %v", err)
			}
			if _, err := os.Lstat(filepath.Join(state, renderStateProfileName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("dirty state was claimed: %v", err)
			}
		})
	}
}

func TestRenderStateProfileRejectsCorruptionAndSymlink(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	output := filepath.Join(root, "output")
	for _, directory := range []string{project, output} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{"corrupt", func(t *testing.T, state string) {
			if err := os.WriteFile(filepath.Join(state, renderStateProfileName), []byte(`{"profile":"wrong"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", func(t *testing.T, state string) {
			if runtime.GOOS == "windows" {
				t.Skip("symlink creation requires privileges on some Windows hosts")
			}
			target := filepath.Join(state, "target")
			if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(state, renderStateProfileName)); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := filepath.Join(root, test.name)
			if err := os.Mkdir(state, 0o700); err != nil {
				t.Fatal(err)
			}
			test.setup(t, state)
			if err := bindRenderStateProject(context.Background(), state, "project", project, output); !errors.Is(err, ErrRenderStateProjectMismatch) {
				t.Fatalf("profile error = %v", err)
			}
		})
	}
}

func TestRenderStateProfileHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bindRenderStateProject(ctx, t.TempDir(), "project", t.TempDir(), t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled bind = %v", err)
	}
}

func TestRenderStateProfileRejectsRelativeProjectRoot(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "output")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := bindRenderStateProject(context.Background(), root, "project", "relative-project", output); err == nil {
		t.Fatal("relative project root accepted")
	}
}

func TestRenderRootsMustBeDisjoint(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	output := filepath.Join(root, "output")
	input := filepath.Join(root, "input")
	nested := filepath.Join(state, "nested")
	inputNested := filepath.Join(input, "nested")
	for _, directory := range []string{state, output, input, nested, inputNested} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateRenderRootSeparation(state, output, []string{input}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name          string
		state, output string
		inputs        []string
	}{
		{"same state output", state, state, []string{input}},
		{"state input", state, output, []string{state}},
		{"nested state input", state, output, []string{nested}},
		{"output input", state, output, []string{output}},
		{"duplicate inputs", state, output, []string{input, input}},
		{"nested inputs", state, output, []string{input, inputNested}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateRenderRootSeparation(test.state, test.output, test.inputs); err == nil {
				t.Fatal("overlapping roots accepted")
			}
		})
	}
}

func TestRenderRootSeparationRejectsCaseAlias(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "State")
	alias := filepath.Join(root, "state")
	input := filepath.Join(root, "input")
	for _, directory := range []string{state, input} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stateInfo, stateErr := os.Stat(state)
	aliasInfo, aliasErr := os.Stat(alias)
	if stateErr != nil || aliasErr != nil || !os.SameFile(stateInfo, aliasInfo) {
		t.Skip("filesystem is case-sensitive")
	}
	if err := validateRenderRootSeparation(state, alias, []string{input}); err == nil {
		t.Fatal("same directory through a case alias was accepted")
	}
}

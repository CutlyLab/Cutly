package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/CutlyLab/Cutly/agent/ffmpeghost"
	"github.com/CutlyLab/Cutly/agent/projectlocal"
	"github.com/CutlyLab/Cutly/agent/renderexec"
	"github.com/CutlyLab/Cutly/agent/renderplan"
	"github.com/CutlyLab/Cutly/agent/renderprepare"
	"github.com/CutlyLab/Cutly/agent/renderqueue"
	"github.com/CutlyLab/Cutly/agent/router"
	"github.com/CutlyLab/Cutly/cache/cas"
	"github.com/CutlyLab/Cutly/core/videoedit"
	cutlyffprobe "github.com/CutlyLab/Cutly/media/ffprobe"
	"github.com/CutlyLab/Cutly/media/proberegistry"
	"github.com/CutlyLab/Cutly/project/local"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/backendstage"
	checkpointlocal "github.com/CutlyLab/Cutly/render/checkpoint/local"
	"github.com/CutlyLab/Cutly/render/ffmpegbackend"
	"github.com/CutlyLab/Cutly/render/prepared"
	renderruntime "github.com/CutlyLab/Cutly/render/runtime"
	"github.com/CutlyLab/Cutly/render/scheduler"
	schedulerlocal "github.com/CutlyLab/Cutly/render/scheduler/local"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

type application struct {
	handler *router.Router
	render  *renderApplication
}

func buildApplication(ctx context.Context, configuration config) (*application, error) {
	if ctx == nil {
		return nil, errors.New("cutlyd: application context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	routes, projects, err := buildProjectComponents(configuration)
	if err != nil {
		return nil, err
	}
	var render *renderApplication
	if configuration.render.enabled {
		render, err = buildRenderApplication(ctx, configuration, projects)
		if err != nil {
			return nil, err
		}
		routes = append(routes, render.routes...)
	}
	handler, err := router.New(routes...)
	if err != nil {
		if render != nil {
			_ = render.Close()
		}
		return nil, fmt.Errorf("cutlyd: initialize action router: %w", err)
	}
	return &application{handler: handler, render: render}, nil
}

func buildProjectRoutes(configuration config) ([]router.Route, error) {
	routes, _, err := buildProjectComponents(configuration)
	return routes, err
}

func buildProjectComponents(configuration config) ([]router.Route, *projectlocal.Registry, error) {
	if configuration.projectDir == "" {
		return nil, nil, nil
	}
	project, err := local.New(configuration.projectDir, local.Options{})
	if err != nil {
		return nil, nil, fmt.Errorf("open local project: %w", err)
	}
	registry, err := projectlocal.NewRegistry(map[string]*local.Service{configuration.projectID: project})
	if err != nil {
		return nil, nil, fmt.Errorf("register local project: %w", err)
	}
	projectActions, err := projectlocal.New(registry)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize local project actions: %w", err)
	}
	return projectActions.Routes(), registry, nil
}

func (application *application) Errors() <-chan error {
	if application == nil || application.render == nil || application.render.workers == nil {
		return nil
	}
	return application.render.workers.Errors()
}

func (application *application) Close() error {
	if application == nil || application.render == nil {
		return nil
	}
	return application.render.Close()
}

type renderApplication struct {
	routes  []router.Route
	workers *workerPool
	backend *ffmpegbackend.Backend
	journal *schedulerlocal.Journal
	owner   *renderStateOwnerLock

	closeOnce sync.Once
	closeErr  error
}

func buildRenderApplication(
	ctx context.Context,
	configuration config,
	projects *projectlocal.Registry,
) (_ *renderApplication, returnErr error) {
	if projects == nil {
		return nil, errors.New("cutlyd: render mode requires a registered local project")
	}
	render := configuration.render
	for _, directory := range []struct{ name, path string }{
		{"render state", render.stateDir},
		{"render output", render.outputDir},
	} {
		if err := requireRealDirectory(directory.path); err != nil {
			return nil, fmt.Errorf("cutlyd: %s directory: %w", directory.name, err)
		}
	}
	for index, root := range render.inputRoots {
		if err := requireRealDirectory(root); err != nil {
			return nil, fmt.Errorf("cutlyd: render input root %d: %w", index, err)
		}
	}
	if err := validateRenderRootSeparation(render.stateDir, render.outputDir, render.inputRoots); err != nil {
		return nil, err
	}
	if render.mediaRuntimeBundleRoot != "" {
		if err := requireRealDirectory(render.mediaRuntimeBundleRoot); err != nil {
			return nil, fmt.Errorf("cutlyd: media runtime bundle directory: %w", err)
		}
		if err := validateMediaRuntimeRootSeparation(
			render.mediaRuntimeBundleRoot,
			configuration.projectDir,
			render.stateDir,
			render.outputDir,
			render.inputRoots,
		); err != nil {
			return nil, err
		}
	}
	mediaRuntime, err := resolveMediaRuntime(ctx, render)
	if err != nil {
		return nil, fmt.Errorf("cutlyd: initialize media runtime: %w", err)
	}

	owner, err := acquireRenderStateOwner(render.stateDir)
	if err != nil {
		return nil, err
	}
	result := &renderApplication{owner: owner}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, result.Close())
		}
	}()
	if err := bindRenderStateProject(ctx, render.stateDir, configuration.projectID, configuration.projectDir, render.outputDir); err != nil {
		return nil, fmt.Errorf("cutlyd: bind render state project: %w", err)
	}

	probeRegistry, err := proberegistry.New(filepath.Join(render.stateDir, "probes"), proberegistry.Options{})
	if err != nil {
		return nil, fmt.Errorf("cutlyd: initialize probe registry: %w", err)
	}
	prober, err := cutlyffprobe.New(ctx, mediaRuntime.ffprobeExecutable,
		cutlyffprobe.DefaultBuildIdentity(mediaRuntime.ffprobeRuntimeDependencyFingerprint), cutlyffprobe.Options{})
	if err != nil {
		return nil, fmt.Errorf("cutlyd: initialize FFprobe: %w", err)
	}
	if err := mediaRuntime.Revalidate(ctx); err != nil {
		return nil, fmt.Errorf("cutlyd: revalidate media runtime after FFprobe initialization: %w", err)
	}
	backend, err := ffmpegbackend.New(ctx, ffmpegbackend.Options{
		Executable: mediaRuntime.ffmpegExecutable, OutputRoot: render.outputDir, InputRoots: []string(render.inputRoots),
		ProbeResolver: probeRegistry, SDKBuildDigest: render.sdkBuildDigest,
		RuntimeDependencyFingerprint: mediaRuntime.ffmpegRuntimeDependencyFingerprint,
	})
	if err != nil {
		return nil, fmt.Errorf("cutlyd: initialize FFmpeg backend: %w", err)
	}
	result.backend = backend
	if err := mediaRuntime.Revalidate(ctx); err != nil {
		return nil, fmt.Errorf("cutlyd: revalidate media runtime after FFmpeg initialization: %w", err)
	}
	stageBackend, err := backendstage.AdaptFFmpeg(backend)
	if err != nil {
		return nil, fmt.Errorf("cutlyd: adapt FFmpeg backend: %w", err)
	}

	preparedStore, err := prepared.New(filepath.Join(render.stateDir, "prepared"), prepared.Options{})
	if err != nil {
		return nil, fmt.Errorf("cutlyd: initialize prepared store: %w", err)
	}
	contentStore, err := cas.New(filepath.Join(render.stateDir, "cas"), cas.Options{})
	if err != nil {
		return nil, fmt.Errorf("cutlyd: initialize CAS: %w", err)
	}
	checkpoints, err := checkpointlocal.New(filepath.Join(render.stateDir, "checkpoints"), contentStore, checkpointlocal.Options{})
	if err != nil {
		return nil, fmt.Errorf("cutlyd: initialize checkpoints: %w", err)
	}
	journal, err := schedulerlocal.OpenJournal(filepath.Join(render.stateDir, "scheduler.journal"))
	if err != nil {
		return nil, fmt.Errorf("cutlyd: open scheduler journal: %w", err)
	}
	result.journal = journal
	coordinator, err := scheduler.Open(ctx, journal)
	if err != nil {
		return nil, fmt.Errorf("cutlyd: replay scheduler journal: %w", err)
	}
	stageService, err := backendstage.New(backendstage.Config{
		Environments: preparedStore,
		Backends: backendstage.BackendResolverFunc(func(ctx context.Context, _ renderapi.ExecutionManifest) (backendstage.Backend, error) {
			if ctx == nil {
				return nil, errors.New("cutlyd: backend resolution context is required")
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return stageBackend, nil
		}),
		CAS: contentStore, MemoryBytes: render.stageMemoryBytes, DiskBytes: render.stageDiskBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("cutlyd: initialize backend stage: %w", err)
	}
	dispatcher, err := renderruntime.New(renderruntime.Config{
		Coordinator: coordinator, Plans: stageService, Checkpoints: checkpoints,
		Executors: stageService, Artifacts: stageService,
		StageLimits: stagedag.Limits{
			MaxConcurrent: 1, MaxMemoryBytes: render.stageMemoryBytes, MaxDiskBytes: render.stageDiskBytes,
		},
		MaxConcurrentJobs: render.maxConcurrentJobs,
	})
	if err != nil {
		return nil, fmt.Errorf("cutlyd: initialize render dispatcher: %w", err)
	}
	hostResolver, err := ffmpeghost.New(ffmpeghost.Config{
		ProjectID: configuration.projectID, Backend: backend, Prober: prober, Registry: probeRegistry,
		ExpectedCompilerBuild: render.compilerBuild, ExpectedEvaluatorBuild: render.evaluatorBuild,
	})
	if err != nil {
		return nil, fmt.Errorf("cutlyd: initialize host resolver: %w", err)
	}
	planActions, err := renderplan.New(renderplan.Config{
		ProjectID: configuration.projectID,
		Resolver: renderplan.DocumentResolverFunc(func(ctx context.Context, projectID string) (videoedit.Document, error) {
			project, err := projects.ResolveProject(ctx, projectID)
			if err != nil {
				return videoedit.Document{}, err
			}
			snapshot, err := project.Inspect(ctx)
			if err != nil {
				return videoedit.Document{}, err
			}
			return snapshot.Document, nil
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("cutlyd: initialize render plan actions: %w", err)
	}
	prepareActions, err := renderprepare.New(renderprepare.Config{
		ProjectID: configuration.projectID,
		Preparer:  hostResolver,
	})
	if err != nil {
		return nil, fmt.Errorf("cutlyd: initialize render prepare actions: %w", err)
	}
	executionActions, err := renderexec.NewWithRecorder(hostResolver, renderexec.RecorderFunc(
		func(ctx context.Context, projectID string, plan videoedit.RenderPlan, manifest renderapi.ExecutionManifest) error {
			if projectID != configuration.projectID {
				return errors.New("cutlyd: prepared environment is outside the configured project")
			}
			return preparedStore.RecordPrepared(ctx, projectID, plan, manifest)
		},
	))
	if err != nil {
		return nil, fmt.Errorf("cutlyd: initialize render execution actions: %w", err)
	}
	workers, err := startWorkerPool(ctx, dispatcher, render.maxConcurrentJobs, render.workerPollInterval)
	if err != nil {
		return nil, fmt.Errorf("cutlyd: start render workers: %w", err)
	}
	result.workers = workers
	coordinatorWithWake := &notifyingCoordinator{dispatcher: dispatcher, workers: workers}
	queueActions, err := renderqueue.NewScoped(configuration.projectID, coordinatorWithWake, renderqueue.AdmissionFunc(
		func(ctx context.Context, projectID string, job renderapi.RenderJob) error {
			if projectID != configuration.projectID {
				return errors.New("cutlyd: render job is outside the configured project")
			}
			_, err := stageService.ResolveStagePlan(ctx, job)
			return err
		},
	))
	if err != nil {
		return nil, fmt.Errorf("cutlyd: initialize render queue actions: %w", err)
	}
	result.routes = append(planActions.Routes(), prepareActions.Routes()...)
	result.routes = append(result.routes, executionActions.Routes()...)
	result.routes = append(result.routes, queueActions.Routes()...)
	return result, nil
}

func (application *renderApplication) Close() error {
	if application == nil {
		return nil
	}
	application.closeOnce.Do(func() {
		if application.workers != nil {
			application.workers.Close()
		}
		application.closeErr = errors.Join(
			closeFFmpegBackend(application.backend),
			closeSchedulerJournal(application.journal),
			application.owner.Close(),
		)
	})
	return application.closeErr
}

func closeFFmpegBackend(backend *ffmpegbackend.Backend) error {
	if backend == nil {
		return nil
	}
	return backend.Close()
}

func closeSchedulerJournal(journal *schedulerlocal.Journal) error {
	if journal == nil {
		return nil
	}
	return journal.Close()
}

func requireRealDirectory(path string) error {
	information, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !information.IsDir() || information.Mode()&os.ModeSymlink != 0 {
		return errors.New("must be an existing non-symlink directory")
	}
	return nil
}

func validateRenderRootSeparation(stateDir, outputDir string, inputRoots []string) error {
	state, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		return fmt.Errorf("cutlyd: resolve render state root: %w", err)
	}
	output, err := filepath.EvalSymlinks(outputDir)
	if err != nil {
		return fmt.Errorf("cutlyd: resolve render output root: %w", err)
	}
	state = filepath.Clean(state)
	output = filepath.Clean(output)
	overlaps, err := directoriesOverlap(state, output)
	if err != nil {
		return fmt.Errorf("cutlyd: compare render state and output roots: %w", err)
	}
	if overlaps {
		return errors.New("cutlyd: render state and output roots must be disjoint")
	}
	resolvedInputs := make([]string, 0, len(inputRoots))
	for index, inputRoot := range inputRoots {
		input, resolveErr := filepath.EvalSymlinks(inputRoot)
		if resolveErr != nil {
			return fmt.Errorf("cutlyd: resolve render input root %d: %w", index, resolveErr)
		}
		input = filepath.Clean(input)
		stateOverlap, stateErr := directoriesOverlap(state, input)
		outputOverlap, outputErr := directoriesOverlap(output, input)
		if err := errors.Join(stateErr, outputErr); err != nil {
			return fmt.Errorf("cutlyd: compare render input root %d: %w", index, err)
		}
		if stateOverlap || outputOverlap {
			return fmt.Errorf("cutlyd: render input root %d must be disjoint from state and output roots", index)
		}
		for priorIndex, prior := range resolvedInputs {
			overlaps, compareErr := directoriesOverlap(prior, input)
			if compareErr != nil {
				return fmt.Errorf("cutlyd: compare render input roots %d and %d: %w", priorIndex, index, compareErr)
			}
			if overlaps {
				return fmt.Errorf("cutlyd: render input roots %d and %d must be disjoint", priorIndex, index)
			}
		}
		resolvedInputs = append(resolvedInputs, input)
	}
	return nil
}

func directoriesOverlap(left, right string) (bool, error) {
	leftInfo, err := os.Stat(left)
	if err != nil {
		return false, err
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		return false, err
	}
	if os.SameFile(leftInfo, rightInfo) {
		return true, nil
	}
	leftContainsRight, err := directoryContains(leftInfo, right)
	if err != nil || leftContainsRight {
		return leftContainsRight, err
	}
	return directoryContains(rightInfo, left)
}

func directoryContains(root os.FileInfo, candidate string) (bool, error) {
	current := filepath.Clean(candidate)
	for depth := 0; depth < 1024; depth++ {
		information, err := os.Stat(current)
		if err != nil {
			return false, err
		}
		if os.SameFile(root, information) {
			return true, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false, nil
		}
		current = parent
	}
	return false, errors.New("directory ancestry exceeds the fixed depth limit")
}

type notifyingCoordinator struct {
	dispatcher renderqueue.Coordinator
	workers    interface{ Notify() }
}

func (coordinator *notifyingCoordinator) Submit(ctx context.Context, job renderapi.RenderJob) (scheduler.JobSnapshot, bool, error) {
	snapshot, created, err := coordinator.dispatcher.Submit(ctx, job)
	// Also wake on an idempotent replay. A worker may have observed an empty
	// queue immediately before a client replayed an already-durable queued job;
	// relying only on the fallback ticker would add avoidable latency.
	if err == nil {
		coordinator.workers.Notify()
	}
	return snapshot, created, err
}

func (coordinator *notifyingCoordinator) Snapshot(jobID string) (scheduler.JobSnapshot, error) {
	return coordinator.dispatcher.Snapshot(jobID)
}

func (coordinator *notifyingCoordinator) History(jobID string, afterSequence uint64, limit int) (scheduler.JobEventPage, error) {
	return coordinator.dispatcher.History(jobID, afterSequence, limit)
}

func (coordinator *notifyingCoordinator) RequestCancel(ctx context.Context, jobID string) (scheduler.JobSnapshot, bool, error) {
	snapshot, changed, err := coordinator.dispatcher.RequestCancel(ctx, jobID)
	if err == nil {
		coordinator.workers.Notify()
	}
	return snapshot, changed, err
}

var _ renderqueue.Coordinator = (*notifyingCoordinator)(nil)

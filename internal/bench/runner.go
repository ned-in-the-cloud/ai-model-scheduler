package bench

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/nomad/api"

	"ai-model-scheduler/internal/hardware"
	"ai-model-scheduler/internal/jobspec"
	"ai-model-scheduler/internal/nomadapi"
)

// Cluster is what the runner needs from Nomad, narrowed so the run loop can
// be tested against a fake.
type Cluster interface {
	ListManaged() ([]nomadapi.Deployment, error)
	// DeploymentParams returns the stored parameters of a managed deployment.
	DeploymentParams(name string) (jobspec.Params, bool)
	Deploy(p jobspec.Params) error
	Stop(name string, purge bool) error

	RegisterJob(job *api.Job) error
	// BatchStatus returns the latest allocation of a batch job and its client
	// status ("" when no allocation exists yet).
	BatchStatus(jobID string) (allocID, status string, err error)
	ReadLogs(allocID, stream string, all bool) (string, error)
	FailureReason(allocID string) string
	PurgeJob(jobID string) error
	PurgeJobsWithPrefix(prefix string) error

	GPUStats(ctx context.Context) ([]hardware.GPUStats, error)
}

// Runner executes benchmark runs, one at a time.
type Runner struct {
	store   *Store
	cluster Cluster
	env     jobspec.Env
	log     *slog.Logger

	// Poll is how often the runner checks on deployments and eval jobs.
	Poll time.Duration
	// EvalTimeout bounds one config's eval job after the endpoint is ready.
	EvalTimeout time.Duration

	mu     sync.Mutex
	active string // ID of the run in progress
	cancel context.CancelFunc
	done   chan struct{}
}

// NewRunner wires a runner to a store and cluster.
func NewRunner(store *Store, cluster Cluster, env jobspec.Env, log *slog.Logger) *Runner {
	return &Runner{
		store: store, cluster: cluster, env: env, log: log,
		Poll: 3 * time.Second, EvalTimeout: 3 * time.Hour,
	}
}

// deploymentPrefix names the temporary deployments a run creates.
const deploymentPrefix = "bench-"

// ErrRunActive is returned when a run is requested while one is in progress.
var ErrRunActive = errors.New("a benchmark run is already in progress")

// Start creates a run for the suite and executes it in the background.
func (r *Runner) Start(suiteID string, eval EvalSpec) (*Run, error) {
	suite, err := r.store.GetSuite(suiteID)
	if err != nil {
		return nil, fmt.Errorf("loading suite: %w", err)
	}
	if err := eval.Normalize(); err != nil {
		return nil, err
	}
	if eval.Accuracy == AccuracyJSONL && eval.DatasetID != "" {
		if _, err := r.store.GetDataset(eval.DatasetID); err != nil {
			return nil, fmt.Errorf("dataset %s: %w", eval.DatasetID, err)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != "" {
		return nil, ErrRunActive
	}
	run := &Run{
		SuiteID: suite.ID, SuiteName: suite.Name, Eval: eval,
		Status: RunPending, StartedAt: time.Now().UTC(),
	}
	for i, c := range suite.Configs {
		run.Results = append(run.Results, ConfigResult{Index: i, Label: c.Label, Config: c, Status: ConfigPending})
	}
	if err := r.store.SaveRun(run); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.active, r.cancel, r.done = run.ID, cancel, make(chan struct{})
	go r.execute(ctx, run)
	return run, nil
}

// Cancel stops the active run. The runner tears down the current config
// and restores prior deployments before marking the run cancelled.
func (r *Runner) Cancel(runID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != runID {
		return fmt.Errorf("run %s is not active", runID)
	}
	r.cancel()
	return nil
}

// ActiveID returns the ID of the run in progress, or "".
func (r *Runner) ActiveID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// Wait blocks until the active run (if any) finishes. For tests.
func (r *Runner) Wait() {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (r *Runner) save(run *Run) {
	if err := r.store.SaveRun(run); err != nil {
		r.log.Error("saving benchmark run", "id", run.ID, "err", err)
	}
}

func (r *Runner) finish(run *Run, status string) {
	run.Status = status
	run.FinishedAt = time.Now().UTC()
	r.save(run)
	r.mu.Lock()
	r.active, r.cancel = "", nil
	close(r.done)
	r.mu.Unlock()
	r.log.Info("benchmark run finished", "id", run.ID, "status", status)
}

// execute is the run loop: snapshot and stop other deployments, then for
// each config deploy → eval → tear down, then restore.
func (r *Runner) execute(ctx context.Context, run *Run) {
	run.Status = RunRunning
	r.save(run)

	if err := r.stopOthers(run); err != nil {
		run.Error = err.Error()
		for i := range run.Results {
			run.Results[i].Status = ConfigSkipped
		}
		r.restore(run)
		r.finish(run, RunFailed)
		return
	}

	for i := range run.Results {
		if ctx.Err() != nil {
			run.Results[i].Status = ConfigSkipped
			continue
		}
		r.runConfig(ctx, run, &run.Results[i])
		r.save(run)
	}

	r.restore(run)
	if ctx.Err() != nil {
		r.finish(run, RunCancelled)
		return
	}
	r.finish(run, RunComplete)
}

// stopOthers records and stops every running deployment so the box is free.
// Leftover bench deployments from an interrupted run are purged instead.
func (r *Runner) stopOthers(run *Run) error {
	deps, err := r.cluster.ListManaged()
	if err != nil {
		return fmt.Errorf("listing deployments: %w", err)
	}
	for _, d := range deps {
		if strings.HasPrefix(d.Name, deploymentPrefix) {
			_ = r.cluster.Stop(d.Name, true)
			continue
		}
		if d.Status == "dead" {
			continue
		}
		p, ok := r.cluster.DeploymentParams(d.Name)
		if !ok {
			p = jobspec.Params{Name: d.Name, Runtime: d.Runtime, Model: d.Model, GPU: d.GPU, Port: d.Port, CPUMHz: d.CPUMHz, MemMB: d.MemMB}
		}
		if err := r.cluster.Stop(d.Name, false); err != nil {
			return fmt.Errorf("stopping %s: %w", d.Name, err)
		}
		r.log.Info("benchmark: stopped deployment for the run", "name", d.Name)
		run.Restore = append(run.Restore, p)
	}
	r.save(run)
	return nil
}

// restore relaunches the deployments stopped by stopOthers.
func (r *Runner) restore(run *Run) {
	if run.Restored {
		return
	}
	var errs []string
	for _, p := range run.Restore {
		if err := r.cluster.Deploy(p); err != nil && !strings.Contains(err.Error(), "already exists") {
			errs = append(errs, fmt.Sprintf("relaunch %s: %v", p.Name, err))
			r.log.Warn("benchmark: relaunch failed", "name", p.Name, "err", err)
		} else {
			r.log.Info("benchmark: relaunched deployment", "name", p.Name)
		}
	}
	run.Restored = true
	if len(errs) > 0 {
		run.Error = strings.TrimSpace(run.Error + "\n" + strings.Join(errs, "\n"))
	}
	r.save(run)
}

func (r *Runner) runConfig(ctx context.Context, run *Run, res *ConfigResult) {
	name := fmt.Sprintf("%s%s-%d", deploymentPrefix, Short(run.ID), res.Index)
	res.Deployment = name
	res.StartedAt = time.Now().UTC()
	res.Status = ConfigDeploying
	r.save(run)

	fail := func(format string, args ...any) {
		res.Status = ConfigFailed
		res.Error = fmt.Sprintf(format, args...)
		res.FinishedAt = time.Now().UTC()
		r.log.Warn("benchmark config failed", "run", run.ID, "label", res.Label, "err", res.Error)
	}
	defer func() {
		if err := r.cluster.Stop(name, true); err != nil {
			r.log.Warn("benchmark: purging deployment", "name", name, "err", err)
		}
	}()

	params := res.Config.Params(name)
	deployStart := time.Now()
	if err := r.cluster.Deploy(params); err != nil {
		fail("deploy: %v", err)
		return
	}

	readyTimeout := time.Duration(run.Eval.ReadyTimeoutSec) * time.Second
	endpoint, err := r.waitHealthy(ctx, name, readyTimeout)
	if err != nil {
		fail("%v", err)
		return
	}
	allocSec := time.Since(deployStart).Seconds()

	res.Status = ConfigEvaluating
	r.save(run)

	jobID := jobspec.BenchJobPrefix + Short(run.ID) + "-" + fmt.Sprint(res.Index)
	spec := jobspec.BenchEval{
		JobID: jobID, Endpoint: endpoint, ModelName: name, Runtime: res.Config.Runtime,
		Accuracy: run.Eval.Accuracy, DatasetPath: run.Eval.DatasetPath,
		Tasks: run.Eval.Tasks, Limit: run.Eval.Limit,
		Concurrency: run.Eval.Concurrency, Prompts: run.Eval.Prompts, MaxTokens: run.Eval.MaxTokens,
		ReadyTimeoutSec: run.Eval.ReadyTimeoutSec,
		OutDir:          path.Join(jobspec.BenchDir, run.ID, fmt.Sprint(res.Index)),
	}
	if run.Eval.Accuracy == AccuracyJSONL && run.Eval.DatasetID != "" {
		data, err := r.store.ReadDataset(run.Eval.DatasetID)
		if err != nil {
			fail("reading dataset: %v", err)
			return
		}
		spec.DatasetInline = string(data)
	}
	if err := r.cluster.RegisterJob(jobspec.BenchEvalJob(r.env, spec)); err != nil {
		fail("starting eval job: %v", err)
		return
	}
	defer func() {
		if err := r.cluster.PurgeJob(jobID); err != nil {
			r.log.Warn("benchmark: purging eval job", "job", jobID, "err", err)
		}
	}()

	metrics, resources, err := r.watchEval(ctx, run, res, jobID, readyTimeout+r.EvalTimeout)
	if err != nil {
		fail("%v", err)
		return
	}
	metrics.Resources = resources
	res.Metrics = metrics
	res.StartupSec = allocSec + metrics.WaitSec
	res.Status = ConfigComplete
	res.Progress = ""
	res.FinishedAt = time.Now().UTC()
	r.log.Info("benchmark config complete", "run", run.ID, "label", res.Label)
}

// waitHealthy polls until the deployment's allocation is running with an
// endpoint, or the timeout / context expires.
func (r *Runner) waitHealthy(ctx context.Context, name string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var last nomadapi.Deployment
	for {
		deps, err := r.cluster.ListManaged()
		if err == nil {
			for _, d := range deps {
				if d.Name != name {
					continue
				}
				last = d
				if d.Healthy && d.Endpoint != "" {
					return d.Endpoint, nil
				}
				if d.AllocStatus == "failed" || d.Status == "dead" {
					reason := ""
					if d.AllocID != "" {
						reason = r.cluster.FailureReason(d.AllocID)
					}
					return "", fmt.Errorf("deployment failed: %s", firstLine(reason))
				}
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("deployment not healthy after %s (last status %s/%s)", timeout, last.Status, last.AllocStatus)
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("cancelled")
		case <-time.After(r.Poll):
		}
	}
}

// watchEval polls the eval job until it ends, recording progress lines and
// sampling GPU usage along the way.
func (r *Runner) watchEval(ctx context.Context, run *Run, res *ConfigResult, jobID string, timeout time.Duration) (*Metrics, *Resources, error) {
	deadline := time.Now().Add(timeout)
	resources := &Resources{}
	var utilSum float64
	vendor := res.Config.GPU
	for {
		allocID, status, err := r.cluster.BatchStatus(jobID)
		if err == nil && allocID != "" {
			switch status {
			case "complete":
				stdout, err := r.cluster.ReadLogs(allocID, "stdout", true)
				if err != nil && stdout == "" {
					return nil, nil, fmt.Errorf("reading eval output: %w", err)
				}
				m, err := ParseEvalOutput(stdout)
				if err != nil {
					return nil, nil, err
				}
				if resources.Samples > 0 {
					resources.AvgUtil = utilSum / float64(resources.Samples)
				}
				return m, resources, nil
			case "failed":
				return nil, nil, fmt.Errorf("eval job failed: %s", firstLine(r.cluster.FailureReason(allocID)))
			case "running":
				if tail, err := r.cluster.ReadLogs(allocID, "stdout", false); err == nil {
					if p := LastProgress(tail); p != "" && p != res.Progress {
						res.Progress = p
						r.save(run)
					}
				}
				if vendor != "" {
					if gpus, err := r.cluster.GPUStats(ctx); err == nil {
						var vram int64
						var util float64
						n := 0
						for _, g := range gpus {
							if g.Vendor != vendor {
								continue
							}
							vram += g.VRAMUsed
							if g.UtilPercent >= 0 {
								util += g.UtilPercent
								n++
							}
						}
						if vram > resources.PeakVRAMBytes {
							resources.PeakVRAMBytes = vram
						}
						if n > 0 {
							utilSum += util / float64(n)
							resources.Samples++
						}
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, nil, fmt.Errorf("eval did not finish within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return nil, nil, fmt.Errorf("cancelled")
		case <-time.After(r.Poll):
		}
	}
}

// Recover handles runs left in progress by an app restart: marks them
// interrupted, removes leftover bench jobs, and relaunches what was stopped.
func (r *Runner) Recover() {
	runs, err := r.store.ListRuns()
	if err != nil {
		r.log.Warn("benchmark recovery: listing runs", "err", err)
		return
	}
	for i := range runs {
		run := &runs[i]
		if !run.Active() {
			continue
		}
		r.log.Warn("benchmark run interrupted by restart", "id", run.ID)
		for j := range run.Results {
			switch run.Results[j].Status {
			case ConfigPending:
				run.Results[j].Status = ConfigSkipped
			case ConfigDeploying, ConfigEvaluating:
				run.Results[j].Status = ConfigFailed
				run.Results[j].Error = "interrupted by app restart"
			}
		}
		if deps, err := r.cluster.ListManaged(); err == nil {
			for _, d := range deps {
				if strings.HasPrefix(d.Name, deploymentPrefix) {
					_ = r.cluster.Stop(d.Name, true)
				}
			}
		}
		_ = r.cluster.PurgeJobsWithPrefix(jobspec.BenchJobPrefix)
		r.restore(run)
		run.Status = RunInterrupted
		run.FinishedAt = time.Now().UTC()
		r.save(run)
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		// Prefer the last line for stderr tails, where the final error is.
		lines := strings.Split(s, "\n")
		return strings.TrimSpace(lines[len(lines)-1])
	}
	return s
}

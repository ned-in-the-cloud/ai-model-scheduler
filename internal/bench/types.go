// Package bench runs model configuration benchmarks: a suite of deployment
// configurations is deployed one at a time, an eval batch job on the box
// measures each one, and the results are stored locally for side-by-side
// comparison.
package bench

import (
	"fmt"
	"strings"
	"time"

	"ai-model-scheduler/internal/jobspec"
)

// Config is one deployment configuration in a suite. It carries the same
// settings as the deploy form; name and port are assigned per run.
type Config struct {
	Label     string            `json:"label" yaml:"label"`
	Runtime   string            `json:"runtime" yaml:"runtime"`
	Model     string            `json:"model" yaml:"model"`
	GPU       string            `json:"gpu,omitempty" yaml:"gpu,omitempty"`
	CtxSize   int               `json:"ctx_size,omitempty" yaml:"ctx_size,omitempty"`
	Threads   int               `json:"threads,omitempty" yaml:"threads,omitempty"`
	GPULayers int               `json:"gpu_layers,omitempty" yaml:"gpu_layers,omitempty"`
	Env       map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	ExtraArgs string            `json:"extra_args,omitempty" yaml:"extra_args,omitempty"`
	CPUMHz    int               `json:"cpu_mhz,omitempty" yaml:"cpu_mhz,omitempty"`
	MemMB     int               `json:"mem_mb,omitempty" yaml:"mem_mb,omitempty"`
}

// Params converts the config into deployment parameters for a run.
func (c Config) Params(name string) jobspec.Params {
	return jobspec.Params{
		Name: name, Runtime: c.Runtime, Model: c.Model, GPU: c.GPU,
		CtxSize: c.CtxSize, Threads: c.Threads, GPULayers: c.GPULayers,
		Env: c.Env, ExtraArgs: c.ExtraArgs, CPUMHz: c.CPUMHz, MemMB: c.MemMB,
	}
}

// Validate checks the config independent of cluster state. The label must
// be present because it is how columns are identified in comparisons.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Label) == "" {
		return fmt.Errorf("label is required")
	}
	if len(c.Label) > 60 {
		return fmt.Errorf("label %q is too long (max 60)", c.Label)
	}
	// Validate the deployment fields with a placeholder name.
	return c.Params("bench-validate").Validate()
}

// Suite is a named, reusable list of configurations.
type Suite struct {
	ID        string    `json:"id" yaml:"-"`
	Name      string    `json:"name" yaml:"name"`
	Configs   []Config  `json:"configs" yaml:"configs"`
	CreatedAt time.Time `json:"created_at" yaml:"-"`
	UpdatedAt time.Time `json:"updated_at" yaml:"-"`
}

// Validate checks the suite and every config in it.
func (s Suite) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("suite name is required")
	}
	if len(s.Configs) == 0 {
		return fmt.Errorf("suite has no configs")
	}
	seen := map[string]bool{}
	for i, c := range s.Configs {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("config %d (%s): %w", i+1, c.Label, err)
		}
		if seen[c.Label] {
			return fmt.Errorf("duplicate label %q", c.Label)
		}
		seen[c.Label] = true
	}
	return nil
}

// Accuracy evaluation kinds.
const (
	AccuracyNone   = "none"
	AccuracyJSONL  = "jsonl"
	AccuracyLMEval = "lmeval"
)

// EvalSpec describes what a run measures against each configuration.
type EvalSpec struct {
	// Accuracy selects the accuracy evaluation: none, jsonl, or lmeval.
	Accuracy string `json:"accuracy"`
	// DatasetID names an uploaded JSONL dataset (Accuracy=jsonl).
	DatasetID string `json:"dataset_id,omitempty"`
	// DatasetPath is a JSONL file on the model share, relative to its root,
	// for datasets too large to upload (Accuracy=jsonl).
	DatasetPath string `json:"dataset_path,omitempty"`
	// Tasks are lm-eval-harness task names (Accuracy=lmeval).
	Tasks []string `json:"tasks,omitempty"`
	// Limit caps samples per lm-eval task; 0 = whole dataset.
	Limit int `json:"limit,omitempty"`

	// Concurrency levels for the load test; empty = [1 4 8].
	Concurrency []int `json:"concurrency,omitempty"`
	// Prompts is the number of requests per concurrency level (and for the
	// single-stream measurement); 0 = 8.
	Prompts int `json:"prompts,omitempty"`
	// MaxTokens per generated response in the performance tests; 0 = 256.
	MaxTokens int `json:"max_tokens,omitempty"`
	// ReadyTimeoutSec bounds how long a config may take to become healthy
	// and answer its first request (including image pull); 0 = 900.
	ReadyTimeoutSec int `json:"ready_timeout_sec,omitempty"`
}

// Defaults for EvalSpec fields left zero.
var (
	DefaultConcurrency  = []int{1, 4, 8}
	DefaultPrompts      = 8
	DefaultMaxTokens    = 256
	DefaultReadyTimeout = 900
)

// Normalize fills defaults and validates the spec.
func (e *EvalSpec) Normalize() error {
	switch e.Accuracy {
	case "", AccuracyNone:
		e.Accuracy = AccuracyNone
	case AccuracyJSONL:
		if e.DatasetID == "" && e.DatasetPath == "" {
			return fmt.Errorf("jsonl accuracy needs a dataset")
		}
		if e.DatasetPath != "" && (strings.Contains(e.DatasetPath, "..") || strings.HasPrefix(e.DatasetPath, "/")) {
			return fmt.Errorf("invalid dataset path %q", e.DatasetPath)
		}
	case AccuracyLMEval:
		if len(e.Tasks) == 0 {
			return fmt.Errorf("lm-eval accuracy needs at least one task")
		}
		for _, t := range e.Tasks {
			if !taskNameRe.MatchString(t) {
				return fmt.Errorf("invalid task name %q", t)
			}
		}
	default:
		return fmt.Errorf("unknown accuracy kind %q", e.Accuracy)
	}
	if len(e.Concurrency) == 0 {
		e.Concurrency = append([]int(nil), DefaultConcurrency...)
	}
	for _, n := range e.Concurrency {
		if n < 1 || n > 64 {
			return fmt.Errorf("concurrency level %d out of range 1-64", n)
		}
	}
	if e.Prompts <= 0 {
		e.Prompts = DefaultPrompts
	}
	if e.MaxTokens <= 0 {
		e.MaxTokens = DefaultMaxTokens
	}
	if e.ReadyTimeoutSec <= 0 {
		e.ReadyTimeoutSec = DefaultReadyTimeout
	}
	if e.Limit < 0 {
		e.Limit = 0
	}
	return nil
}

// Run statuses.
const (
	RunPending     = "pending"
	RunRunning     = "running"
	RunComplete    = "complete"
	RunCancelled   = "cancelled"
	RunInterrupted = "interrupted" // the app restarted mid-run
	RunFailed      = "failed"      // could not start at all
)

// Config result statuses.
const (
	ConfigPending    = "pending"
	ConfigDeploying  = "deploying"
	ConfigEvaluating = "evaluating"
	ConfigComplete   = "complete"
	ConfigFailed     = "failed"
	ConfigSkipped    = "skipped" // run cancelled/interrupted before reaching it
)

// Run is one execution of a suite.
type Run struct {
	ID         string    `json:"id"`
	SuiteID    string    `json:"suite_id"`
	SuiteName  string    `json:"suite_name"`
	Eval       EvalSpec  `json:"eval"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	// Restore lists the deployments that were running when the run started
	// and are relaunched when it ends.
	Restore  []jobspec.Params `json:"restore,omitempty"`
	Restored bool             `json:"restored"`
	Results  []ConfigResult   `json:"results"`
}

// Active reports whether the run is still in progress.
func (r Run) Active() bool { return r.Status == RunPending || r.Status == RunRunning }

// ConfigResult is the outcome for one configuration in a run.
type ConfigResult struct {
	Index      int       `json:"index"`
	Label      string    `json:"label"`
	Config     Config    `json:"config"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	Progress   string    `json:"progress,omitempty"` // last progress line from the eval job
	Deployment string    `json:"deployment,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	// StartupSec is deploy-to-first-response: allocation start plus the
	// eval job's wait for the endpoint to answer.
	StartupSec float64  `json:"startup_sec,omitempty"`
	Metrics    *Metrics `json:"metrics,omitempty"`
}

// Metrics is what the eval job reports, plus GPU usage sampled by the app.
type Metrics struct {
	Single      *SingleStream `json:"single,omitempty"`
	Concurrency []ConcResult  `json:"concurrency,omitempty"`
	Accuracy    *Accuracy     `json:"accuracy,omitempty"`
	Resources   *Resources    `json:"resources,omitempty"`
	WaitSec     float64       `json:"wait_sec,omitempty"` // eval job's wait for readiness
	Errors      []string      `json:"errors,omitempty"`
}

// SingleStream is the sequential, one-request-at-a-time measurement.
type SingleStream struct {
	TokPerSec float64 `json:"tok_per_sec"`
	TTFTP50Ms float64 `json:"ttft_ms_p50"`
	TTFTP95Ms float64 `json:"ttft_ms_p95"`
	Requests  int     `json:"requests"`
	Errors    int     `json:"errors"`
}

// ConcResult is the aggregate at one concurrency level.
type ConcResult struct {
	N            int     `json:"n"`
	TokPerSec    float64 `json:"tok_per_sec"`
	LatencyP50Ms float64 `json:"latency_ms_p50"`
	LatencyP95Ms float64 `json:"latency_ms_p95"`
	Requests     int     `json:"requests"`
	Errors       int     `json:"errors"`
}

// Accuracy is the accuracy evaluation result.
type Accuracy struct {
	Kind    string               `json:"kind"`
	Score   float64              `json:"score"` // 0..1 overall
	Correct int                  `json:"correct,omitempty"`
	Total   int                  `json:"total,omitempty"`
	Tasks   map[string]TaskScore `json:"tasks,omitempty"`
	// Samples are per-prompt outcomes for JSONL evals (capped by the job).
	Samples []Sample `json:"samples,omitempty"`
}

// TaskScore is one lm-eval task's headline metric.
type TaskScore struct {
	Metric string  `json:"metric"`
	Value  float64 `json:"value"`
}

// Sample is one JSONL prompt outcome.
type Sample struct {
	Prompt   string `json:"prompt"`
	Expected string `json:"expected"`
	Response string `json:"response"`
	Correct  bool   `json:"correct"`
}

// Resources is GPU usage sampled from the stats agent while evaluating.
type Resources struct {
	PeakVRAMBytes int64   `json:"peak_vram_bytes"`
	AvgUtil       float64 `json:"avg_util_percent"`
	Samples       int     `json:"samples"`
}

// Dataset is an uploaded JSONL evaluation set.
type Dataset struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Rows      int       `json:"rows"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

// LMEvalTask is a curated lm-eval-harness task the UI offers.
type LMEvalTask struct {
	Name        string
	Description string
}

// CuratedTasks are generation-based harness tasks that work against an
// OpenAI-compatible chat endpoint (loglikelihood tasks need logprobs, which
// not every runtime exposes).
var CuratedTasks = []LMEvalTask{
	{"gsm8k", "Grade-school math word problems, exact-match on the final number"},
	{"gsm8k_cot", "GSM8K with chain-of-thought prompting"},
	{"truthfulqa_gen", "Truthfulness of free-form answers"},
	{"ifeval", "Instruction-following with verifiable constraints"},
	{"mmlu_flan_cot_zeroshot", "MMLU subjects, zero-shot chain-of-thought (slow: many subtasks)"},
}

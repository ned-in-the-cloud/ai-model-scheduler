package jobspec

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/hashicorp/nomad/api"

	"ai-model-scheduler/internal/nomadapi"
)

// BenchJobPrefix prefixes every benchmark eval job ID.
const BenchJobPrefix = "ams-bench-"

// BenchDir is the folder on the model share where eval jobs write a copy of
// their results and cache lm-eval datasets.
const BenchDir = "_benchmarks"

// BenchEval describes one eval job: what endpoint to measure and how.
type BenchEval struct {
	JobID     string
	Endpoint  string // host:port of the model server
	ModelName string // served model name (deployment name)
	Runtime   string

	Accuracy string // none | jsonl | lmeval | tokens
	// DatasetParts is a directory, relative to the model root, holding an
	// uploaded dataset staged by BenchStageJob: gzip+base64 split into
	// part-NNNN files. DatasetSHA256 is the decoded JSONL's checksum.
	DatasetParts  string
	DatasetSHA256 string
	DatasetPath   string   // JSONL path relative to the model root (jsonl|tokens)
	Tasks         []string // lm-eval task names (lmeval)
	Limit         int      // samples per task, or dataset rows (tokens); 0 = all

	Concurrency     []int
	Prompts         int
	MaxTokens       int
	ReadyTimeoutSec int

	OutDir string // results copy destination, relative to the model root
}

// Template delimiters chosen so dataset content is passed through verbatim:
// consul-template would otherwise interpret any {{ }} in prompts.
const (
	benchTmplLeft  = "[[[[ams"
	benchTmplRight = "ams]]]]"
)

// BenchEvalJob builds the batch job that runs the bench image against one
// deployed configuration. Results come back on stdout (RESULT line) and a
// copy lands on the share under BenchDir.
func BenchEvalJob(env Env, spec BenchEval) *api.Job {
	conc := make([]string, len(spec.Concurrency))
	for i, n := range spec.Concurrency {
		conc[i] = strconv.Itoa(n)
	}
	taskEnv := map[string]string{
		"BENCH_ENDPOINT":      "http://" + spec.Endpoint,
		"BENCH_MODEL":         spec.ModelName,
		"BENCH_RUNTIME":       spec.Runtime,
		"BENCH_ACCURACY":      spec.Accuracy,
		"BENCH_TASKS":         strings.Join(spec.Tasks, ","),
		"BENCH_LIMIT":         strconv.Itoa(spec.Limit),
		"BENCH_CONCURRENCY":   strings.Join(conc, ","),
		"BENCH_PROMPTS":       strconv.Itoa(spec.Prompts),
		"BENCH_MAX_TOKENS":    strconv.Itoa(spec.MaxTokens),
		"BENCH_READY_TIMEOUT": strconv.Itoa(spec.ReadyTimeoutSec),
		"BENCH_OUT_DIR":       path.Join(env.ModelMount, spec.OutDir),
		// Cache harness datasets on the share so repeat runs skip the download.
		"HF_HOME": path.Join(env.ModelMount, BenchDir, ".hf-cache"),
	}
	if spec.DatasetParts != "" {
		taskEnv["BENCH_DATASET_PARTS"] = path.Join(env.ModelMount, spec.DatasetParts)
		taskEnv["BENCH_DATASET_SHA256"] = spec.DatasetSHA256
	} else if spec.DatasetPath != "" {
		taskEnv["BENCH_DATASET"] = path.Join(env.ModelMount, spec.DatasetPath)
	}

	task := &api.Task{
		Name:   nomadapi.TaskName,
		Driver: env.Driver,
		Config: map[string]any{
			"image":   env.Images.Bench,
			"volumes": []string{fmt.Sprintf("%s:%s", env.ModelRootHost, env.ModelMount)},
		},
		Env: taskEnv,
		// lm-eval imports torch; give it room.
		Resources: &api.Resources{CPU: ptr(1000), MemoryMB: ptr(4096)},
	}
	if env.ImagePullTimeout != "" {
		task.Config["image_pull_timeout"] = env.ImagePullTimeout
	}
	return benchBatchJob(spec.JobID, "eval", task)
}

// BenchStage describes one dataset staging job: it writes Content to
// Dir/Name on the model share.
type BenchStage struct {
	JobID   string
	Dir     string // relative to the model root
	Name    string
	Content string // text; the caller base64-encodes binary data
}

// BenchStageJob builds a small batch job that copies inline content onto
// the model share. The app cannot write the share itself, and a Nomad job
// definition can only carry a few hundred KB, so large uploads are split
// across several of these.
func BenchStageJob(env Env, spec BenchStage) *api.Job {
	task := &api.Task{
		Name:   nomadapi.TaskName,
		Driver: env.Driver,
		Config: map[string]any{
			"image":   env.Images.Bench,
			"volumes": []string{fmt.Sprintf("%s:%s", env.ModelRootHost, env.ModelMount)},
		},
		Env: map[string]string{
			"BENCH_MODE":       "stage",
			"BENCH_STAGE_SRC":  "/local/part",
			"BENCH_STAGE_DEST": path.Join(env.ModelMount, spec.Dir, spec.Name),
		},
		Resources: &api.Resources{CPU: ptr(100), MemoryMB: ptr(128)},
		Templates: []*api.Template{{
			EmbeddedTmpl: ptr(spec.Content),
			DestPath:     ptr("local/part"),
			LeftDelim:    ptr(benchTmplLeft),
			RightDelim:   ptr(benchTmplRight),
			ChangeMode:   ptr("noop"),
		}},
	}
	if env.ImagePullTimeout != "" {
		task.Config["image_pull_timeout"] = env.ImagePullTimeout
	}
	return benchBatchJob(spec.JobID, "stage", task)
}

// benchBatchJob wraps a task in a run-once batch job.
func benchBatchJob(jobID, groupName string, task *api.Task) *api.Job {
	group := &api.TaskGroup{
		Name:          ptr(groupName),
		Count:         ptr(1),
		Tasks:         []*api.Task{task},
		RestartPolicy: &api.RestartPolicy{Attempts: ptr(0), Mode: ptr("fail")},
		// Don't reschedule on failure either: the runner reads the outcome.
		ReschedulePolicy: &api.ReschedulePolicy{Attempts: ptr(0), Unlimited: ptr(false)},
	}
	return &api.Job{
		ID:          ptr(jobID),
		Name:        ptr(jobID),
		Type:        ptr(api.JobTypeBatch),
		Datacenters: []string{"*"},
		TaskGroups:  []*api.TaskGroup{group},
		Meta: map[string]string{
			nomadapi.ManagedByKey: nomadapi.ManagedByValue,
			nomadapi.KindKey:      nomadapi.KindHelper,
		},
	}
}

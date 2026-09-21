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

	Accuracy      string   // none | jsonl | lmeval
	DatasetInline string   // JSONL content shipped inside the job (jsonl)
	DatasetPath   string   // JSONL path relative to the model root (jsonl)
	Tasks         []string // lm-eval task names (lmeval)
	Limit         int      // samples per task; 0 = all

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
	if spec.DatasetInline != "" {
		taskEnv["BENCH_DATASET"] = "/local/dataset.jsonl"
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
	if spec.DatasetInline != "" {
		task.Templates = []*api.Template{{
			EmbeddedTmpl: ptr(spec.DatasetInline),
			DestPath:     ptr("local/dataset.jsonl"),
			LeftDelim:    ptr(benchTmplLeft),
			RightDelim:   ptr(benchTmplRight),
			ChangeMode:   ptr("noop"),
		}}
	}

	group := &api.TaskGroup{
		Name:          ptr("eval"),
		Count:         ptr(1),
		Tasks:         []*api.Task{task},
		RestartPolicy: &api.RestartPolicy{Attempts: ptr(0), Mode: ptr("fail")},
		// Don't reschedule on failure either: the runner reads the outcome.
		ReschedulePolicy: &api.ReschedulePolicy{Attempts: ptr(0), Unlimited: ptr(false)},
	}
	return &api.Job{
		ID:          ptr(spec.JobID),
		Name:        ptr(spec.JobID),
		Type:        ptr(api.JobTypeBatch),
		Datacenters: []string{"*"},
		TaskGroups:  []*api.TaskGroup{group},
		Meta: map[string]string{
			nomadapi.ManagedByKey: nomadapi.ManagedByValue,
			nomadapi.KindKey:      nomadapi.KindHelper,
		},
	}
}

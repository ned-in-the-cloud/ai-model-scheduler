package jobspec

import (
	"strings"
	"testing"
)

func TestBenchEvalJob(t *testing.T) {
	env := testEnv("podman")
	env.Images.Bench = "img/bench"
	spec := BenchEval{
		JobID: "ams-bench-abc-0", Endpoint: "10.0.0.5:8100", ModelName: "bench-abc-0", Runtime: "vllm",
		Accuracy: "jsonl", DatasetInline: "{\"prompt\":\"{{ not a template }}\",\"expected\":\"x\"}\n",
		Concurrency: []int{1, 4}, Prompts: 8, MaxTokens: 256, ReadyTimeoutSec: 600,
		OutDir: "_benchmarks/run1/0",
	}
	job := BenchEvalJob(env, spec)

	if *job.Type != "batch" || job.ParameterizedJob != nil {
		t.Errorf("expected a plain batch job, got type=%s parameterized=%v", *job.Type, job.ParameterizedJob != nil)
	}
	task := job.TaskGroups[0].Tasks[0]
	if task.Config["image"] != "img/bench" {
		t.Errorf("image = %v", task.Config["image"])
	}
	if vols := task.Config["volumes"].([]string); len(vols) != 1 || vols[0] != "/mnt/models:/models" {
		t.Errorf("share must be mounted read-write for the results copy: %v", vols)
	}
	want := map[string]string{
		"BENCH_ENDPOINT":    "http://10.0.0.5:8100",
		"BENCH_MODEL":       "bench-abc-0",
		"BENCH_ACCURACY":    "jsonl",
		"BENCH_CONCURRENCY": "1,4",
		"BENCH_DATASET":     "/local/dataset.jsonl",
		"BENCH_OUT_DIR":     "/models/_benchmarks/run1/0",
		"HF_HOME":           "/models/_benchmarks/.hf-cache",
	}
	for k, v := range want {
		if task.Env[k] != v {
			t.Errorf("env %s = %q, want %q", k, task.Env[k], v)
		}
	}
	if len(task.Templates) != 1 {
		t.Fatalf("templates = %d, want 1", len(task.Templates))
	}
	tmpl := task.Templates[0]
	if *tmpl.DestPath != "local/dataset.jsonl" || !strings.Contains(*tmpl.EmbeddedTmpl, "{{ not a template }}") {
		t.Errorf("template = %+v", tmpl)
	}
	if *tmpl.LeftDelim == "{{" || *tmpl.LeftDelim == "" {
		t.Errorf("template delimiters must not be the default so dataset braces pass through: %q", *tmpl.LeftDelim)
	}
	if *job.TaskGroups[0].RestartPolicy.Attempts != 0 || *job.TaskGroups[0].ReschedulePolicy.Attempts != 0 {
		t.Error("eval job must not retry: the runner interprets a failure")
	}

	// Path-based dataset: no template, path under the model mount.
	spec.DatasetInline, spec.DatasetPath = "", "_benchmarks/big.jsonl"
	job = BenchEvalJob(env, spec)
	task = job.TaskGroups[0].Tasks[0]
	if len(task.Templates) != 0 || task.Env["BENCH_DATASET"] != "/models/_benchmarks/big.jsonl" {
		t.Errorf("path dataset: templates=%d BENCH_DATASET=%q", len(task.Templates), task.Env["BENCH_DATASET"])
	}
}

package jobspec

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"ai-model-scheduler/internal/config"
	"ai-model-scheduler/internal/nomadapi"
)

func testEnv(driver string) Env {
	return Env{
		Driver:           driver,
		ModelRootHost:    "/mnt/models",
		ModelMount:       "/models",
		ImagePullTimeout: "45m",
		Images: config.Images{
			LlamaCPP:      "img/llamacpp",
			LlamaCPPCUDA:  "img/llamacpp-cuda",
			LlamaCPPROCm:  "img/llamacpp-rocm",
			LlamaCPPIntel: "img/llamacpp-intel",
			VLLM:          "img/vllm",
			VLLMROCm:      "img/vllm-rocm",
			Ollama:        "img/ollama",
			OllamaROCm:    "img/ollama-rocm",
			GPUStats:      "img/debian",
		},
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		p       Params
		wantErr string
	}{
		{name: "valid", p: Params{Name: "llama3-8b", Runtime: "llamacpp", Model: "x.gguf"}},
		{name: "bad name uppercase", p: Params{Name: "Llama", Runtime: "llamacpp", Model: "x"}, wantErr: "name"},
		{name: "bad name leading dash", p: Params{Name: "-x", Runtime: "llamacpp", Model: "x"}, wantErr: "name"},
		{name: "unknown runtime", p: Params{Name: "x", Runtime: "tgi", Model: "x"}, wantErr: "runtime"},
		{name: "unknown gpu vendor", p: Params{Name: "x", Runtime: "llamacpp", Model: "x", GPU: "voodoo"}, wantErr: "gpu vendor"},
		{name: "missing model", p: Params{Name: "x", Runtime: "vllm"}, wantErr: "model"},
		{name: "ollama without model ok", p: Params{Name: "x", Runtime: "ollama"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.p.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLlamaCPPJob(t *testing.T) {
	p := Params{Name: "l3", Runtime: "llamacpp", Model: "sub/l3.gguf", Port: 8001, CtxSize: 4096}
	job, err := Build(p, testEnv("podman"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if *job.ID != "model-l3" || *job.Type != "service" {
		t.Errorf("job identity: ID=%s Type=%s", *job.ID, *job.Type)
	}
	meta := job.Meta
	if meta[nomadapi.ManagedByKey] != nomadapi.ManagedByValue ||
		meta[nomadapi.KindKey] != nomadapi.KindInference ||
		meta[nomadapi.RuntimeKey] != "llamacpp" || meta[nomadapi.ModelKey] != "sub/l3.gguf" {
		t.Errorf("meta tags: %v", meta)
	}

	tg := job.TaskGroups[0]
	port := tg.Networks[0].ReservedPorts[0]
	if port.Label != "api" || port.Value != 8001 || port.To != 8001 {
		t.Errorf("port: %+v", port)
	}

	task := tg.Tasks[0]
	if task.Driver != "podman" {
		t.Errorf("driver = %s", task.Driver)
	}
	if task.Config["image"] != "img/llamacpp" {
		t.Errorf("image = %v", task.Config["image"])
	}
	args := task.Config["args"].([]string)
	for _, want := range [][]string{
		{"-m", "/models/sub/l3.gguf"},
		{"--port", "8001"},
		{"-c", "4096"},
	} {
		if !containsSeq(args, want) {
			t.Errorf("args missing %v: %v", want, args)
		}
	}
	if slices.Contains(args, "--n-gpu-layers") {
		t.Errorf("CPU job should not offload layers: %v", args)
	}
	vols := task.Config["volumes"].([]string)
	if vols[0] != "/mnt/models:/models:ro" {
		t.Errorf("volumes = %v", vols)
	}
	if _, ok := task.Config["devices"]; ok {
		t.Errorf("CPU job should not request devices")
	}
	if task.Config["image_pull_timeout"] != "45m" {
		t.Errorf("image_pull_timeout = %v, want 45m (driver default 5m is too short for GPU images)",
			task.Config["image_pull_timeout"])
	}
}

func TestLlamaCPPGPUJob(t *testing.T) {
	tests := []struct {
		vendor      string
		wantImage   string
		wantDevices []string
	}{
		{vendor: "nvidia", wantImage: "img/llamacpp-cuda", wantDevices: []string{"nvidia.com/gpu=all"}},
		{vendor: "amd", wantImage: "img/llamacpp-rocm", wantDevices: []string{"/dev/kfd", "/dev/dri"}},
		{vendor: "intel", wantImage: "img/llamacpp-intel", wantDevices: []string{"/dev/dri"}},
	}
	for _, tt := range tests {
		t.Run(tt.vendor, func(t *testing.T) {
			p := Params{Name: "l3", Runtime: "llamacpp", Model: "l3.gguf", Port: 8001, GPU: tt.vendor}
			job, err := Build(p, testEnv("podman"))
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			task := job.TaskGroups[0].Tasks[0]
			if task.Config["image"] != tt.wantImage {
				t.Errorf("image = %v, want %v", task.Config["image"], tt.wantImage)
			}
			if got := task.Config["devices"].([]string); !slices.Equal(got, tt.wantDevices) {
				t.Errorf("devices = %v, want %v", got, tt.wantDevices)
			}
			if !containsSeq(task.Config["args"].([]string), []string{"--n-gpu-layers", "999"}) {
				t.Errorf("args missing gpu layers: %v", task.Config["args"])
			}
			if job.Meta[nomadapi.GPUKey] != tt.vendor {
				t.Errorf("gpu meta = %v", job.Meta)
			}
		})
	}
}

func TestVLLMJob(t *testing.T) {
	p := Params{Name: "qwen", Runtime: "vllm", Model: "qwen2-7b", Port: 8002, GPU: "nvidia", CtxSize: 8192}

	for _, tt := range []struct {
		driver     string
		wantShm    any
		workDirKey string
	}{
		{driver: "podman", wantShm: "8g", workDirKey: "working_dir"},
		{driver: "docker", wantShm: 8 << 30, workDirKey: "work_dir"},
	} {
		t.Run(tt.driver, func(t *testing.T) {
			job, err := Build(p, testEnv(tt.driver))
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			task := job.TaskGroups[0].Tasks[0]
			if task.Config["shm_size"] != tt.wantShm {
				t.Errorf("shm_size = %v, want %v", task.Config["shm_size"], tt.wantShm)
			}
			if got := task.Config[tt.workDirKey]; got != "/models/qwen2-7b" {
				t.Errorf("%s = %v, want /models/qwen2-7b", tt.workDirKey, got)
			}
			args := task.Config["args"].([]string)
			for _, want := range [][]string{
				{"--model", "/models/qwen2-7b"},
				{"--served-model-name", "qwen"},
				{"--max-model-len", "8192"},
			} {
				if !containsSeq(args, want) {
					t.Errorf("args missing %v: %v", want, args)
				}
			}
		})
	}
}

func TestVLLMGPURequirements(t *testing.T) {
	env := testEnv("podman")

	if _, err := Build(Params{Name: "q", Runtime: "vllm", Model: "m", GPU: ""}, env); err == nil ||
		!strings.Contains(err.Error(), "requires a GPU") {
		t.Errorf("CPU vllm err = %v, want GPU requirement", err)
	}

	// Intel has no default vLLM image; the error must name the fix.
	if _, err := Build(Params{Name: "q", Runtime: "vllm", Model: "m", GPU: "intel"}, env); err == nil ||
		!strings.Contains(err.Error(), "IMAGE_") {
		t.Errorf("intel vllm err = %v, want unconfigured-image error", err)
	}

	job, err := Build(Params{Name: "q", Runtime: "vllm", Model: "m", GPU: "amd"}, env)
	if err != nil {
		t.Fatalf("amd vllm: %v", err)
	}
	task := job.TaskGroups[0].Tasks[0]
	if task.Config["image"] != "img/vllm-rocm" {
		t.Errorf("image = %v", task.Config["image"])
	}
	if got := task.Config["devices"].([]string); !slices.Equal(got, []string{"/dev/kfd", "/dev/dri"}) {
		t.Errorf("devices = %v", got)
	}
}

func TestOllamaJob(t *testing.T) {
	p := Params{Name: "oll", Runtime: "ollama", Port: 8003}
	job, err := Build(p, testEnv("podman"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	task := job.TaskGroups[0].Tasks[0]
	vols := task.Config["volumes"].([]string)
	if vols[0] != "/mnt/models:/models" {
		t.Errorf("ollama volume should be read-write: %v", vols)
	}
	if task.Env["OLLAMA_MODELS"] != "/models/ollama" {
		t.Errorf("OLLAMA_MODELS = %v", task.Env)
	}
	if task.Env["OLLAMA_HOST"] != "0.0.0.0:8003" {
		t.Errorf("OLLAMA_HOST = %v", task.Env)
	}
}

func TestHFDownloadJob(t *testing.T) {
	job := HFDownload(testEnv("podman"), "hf_secret")
	if *job.ID != HFDownloadJobID || *job.Type != "batch" {
		t.Errorf("job identity: %s/%s", *job.ID, *job.Type)
	}
	if job.ParameterizedJob == nil ||
		!slices.Equal(job.ParameterizedJob.MetaRequired, []string{"repo_id", "dest"}) {
		t.Errorf("parameterized config: %+v", job.ParameterizedJob)
	}
	task := job.TaskGroups[0].Tasks[0]
	if task.Env["HF_TOKEN"] != "hf_secret" {
		t.Errorf("HF_TOKEN not set: %v", task.Env)
	}
	vols := task.Config["volumes"].([]string)
	if vols[0] != "/mnt/models:/models" {
		t.Errorf("download volume must be read-write: %v", vols)
	}
	// Page cache for buffered writes is charged to the task under cgroup v2;
	// a too-small limit OOM-kills large downloads (seen with a 1024MB limit).
	if mem := *task.Resources.MemoryMB; mem < 4096 {
		t.Errorf("download memory = %d MB, want >= 4096", mem)
	}

	// Without a token the env must not be set at all.
	if task := HFDownload(testEnv("podman"), "").TaskGroups[0].Tasks[0]; task.Env["HF_TOKEN"] != "" {
		t.Errorf("unexpected HF_TOKEN: %v", task.Env)
	}
}

func TestIndexerJob(t *testing.T) {
	job := Indexer(testEnv("podman"))
	if *job.ID != IndexerJobID || *job.Type != "batch" {
		t.Errorf("job identity: %s/%s", *job.ID, *job.Type)
	}
	task := job.TaskGroups[0].Tasks[0]
	vols := task.Config["volumes"].([]string)
	if vols[0] != "/mnt/models:/models:ro" {
		t.Errorf("indexer volume must be read-only: %v", vols)
	}
	if task.Config["image_pull_timeout"] != "45m" {
		t.Errorf("helper image_pull_timeout = %v, want 45m", task.Config["image_pull_timeout"])
	}
	script := task.Config["args"].([]string)[1]
	for _, want := range []string{"*.gguf", "config.json", "/models"} {
		if !strings.Contains(script, want) {
			t.Errorf("indexer script missing %q", want)
		}
	}
	assertInterpolationEscaped(t, script)
	assertInterpolationEscaped(t, HFDownload(testEnv("podman"), "").
		TaskGroups[0].Tasks[0].Config["args"].([]string)[1])
}

// assertInterpolationEscaped fails if the script contains a bare ${ that
// Nomad's HCL2 config parsing would try to interpolate, or a $$ that is not
// the $${ escape form (the shell would read it as its PID).
func assertInterpolationEscaped(t *testing.T, script string) {
	t.Helper()
	if strings.Contains(strings.ReplaceAll(script, "$${", ""), "${") {
		t.Errorf("script contains unescaped ${ (Nomad would interpolate it):\n%s", script)
	}
	if strings.Contains(strings.ReplaceAll(script, "$${", ""), "$$") {
		t.Errorf("script contains $$ outside the $${ escape (shell PID expansion):\n%s", script)
	}
}

func TestParamsMetaRoundTrip(t *testing.T) {
	p := Params{
		Name: "l3", Runtime: "llamacpp", Model: "sub/l3.gguf", Port: 8001,
		GPU: "amd", CtxSize: 4096, Threads: 8, GPULayers: 40,
		Env:       map[string]string{"HSA_OVERRIDE_GFX_VERSION": "12.0.1"},
		ExtraArgs: "--flash-attn", CPUMHz: 2000, MemMB: 8192,
	}
	job, err := Build(p, testEnv("podman"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var stored Params
	if err := json.Unmarshal([]byte(job.Meta[nomadapi.ParamsKey]), &stored); err != nil {
		t.Fatalf("params meta not valid JSON: %v", err)
	}
	if !reflect.DeepEqual(stored, p) {
		t.Errorf("params meta = %+v, want %+v", stored, p)
	}
}

func TestTuningOptions(t *testing.T) {
	p := Params{
		Name: "l3", Runtime: "llamacpp", Model: "l3.gguf", Port: 8001,
		GPU: "amd", Threads: 12, GPULayers: 30,
		Env: map[string]string{"HSA_OVERRIDE_GFX_VERSION": "12.0.1"},
	}
	job, err := Build(p, testEnv("podman"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	task := job.TaskGroups[0].Tasks[0]
	args := task.Config["args"].([]string)
	for _, want := range [][]string{{"-t", "12"}, {"--n-gpu-layers", "30"}} {
		if !containsSeq(args, want) {
			t.Errorf("args missing %v: %v", want, args)
		}
	}
	if task.Env["HSA_OVERRIDE_GFX_VERSION"] != "12.0.1" {
		t.Errorf("env not applied: %v", task.Env)
	}

	if err := (Params{Name: "x", Runtime: "llamacpp", Model: "m",
		Env: map[string]string{"BAD-NAME": "1"}}).Validate(); err == nil {
		t.Error("invalid env key accepted")
	}

	// User env must not clobber Ollama's required defaults logic: defaults
	// fill only unset keys, user values win when both exist.
	oj, err := Build(Params{Name: "o", Runtime: "ollama", Port: 8003,
		Env: map[string]string{"OLLAMA_MODELS": "/models/custom", "OLLAMA_KEEP_ALIVE": "10m"}}, testEnv("podman"))
	if err != nil {
		t.Fatalf("Build ollama: %v", err)
	}
	oe := oj.TaskGroups[0].Tasks[0].Env
	if oe["OLLAMA_MODELS"] != "/models/custom" {
		t.Errorf("user env should win: %v", oe)
	}
	if oe["OLLAMA_HOST"] != "0.0.0.0:8003" || oe["OLLAMA_KEEP_ALIVE"] != "10m" {
		t.Errorf("ollama env merge: %v", oe)
	}
}

func TestGPUStatsAgentJob(t *testing.T) {
	for _, tt := range []struct {
		name   string
		nvidia bool
	}{
		{name: "with nvidia", nvidia: true},
		{name: "amd only", nvidia: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			job := GPUStatsAgent(testEnv("podman"), tt.nvidia)
			if *job.ID != GPUStatsJobID || *job.Type != "service" {
				t.Errorf("job identity: %s/%s", *job.ID, *job.Type)
			}
			task := job.TaskGroups[0].Tasks[0]
			if task.Config["image"] != "img/debian" {
				t.Errorf("image = %v", task.Config["image"])
			}
			_, hasDevices := task.Config["devices"]
			if hasDevices != tt.nvidia {
				t.Errorf("devices present = %v, want %v (CDI only when NVIDIA exists)", hasDevices, tt.nvidia)
			}
			script := task.Config["args"].([]string)[1]
			for _, want := range []string{"gpu_busy_percent", "nvidia-smi", `"gpus"`} {
				if !strings.Contains(script, want) {
					t.Errorf("agent script missing %q", want)
				}
			}
			assertInterpolationEscaped(t, script)
			if job.ParameterizedJob != nil {
				t.Error("stats agent must not be parameterized")
			}
		})
	}
}

// containsSeq reports whether want appears as a contiguous subsequence of got.
func containsSeq(got, want []string) bool {
	for i := 0; i+len(want) <= len(got); i++ {
		if slices.Equal(got[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

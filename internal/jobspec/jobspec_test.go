package jobspec

import (
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
		driver  string
		wantShm any
	}{
		{driver: "podman", wantShm: "8g"},
		{driver: "docker", wantShm: 8 << 30},
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

// containsSeq reports whether want appears as a contiguous subsequence of got.
func containsSeq(got, want []string) bool {
	for i := 0; i+len(want) <= len(got); i++ {
		if slices.Equal(got[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

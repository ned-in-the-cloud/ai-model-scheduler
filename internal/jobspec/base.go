// Package jobspec builds Nomad job definitions for inference runtimes and
// helper jobs. Builders are pure functions from parameters to *api.Job so
// they can be unit-tested without a Nomad cluster.
package jobspec

import (
	"fmt"
	"regexp"

	"github.com/hashicorp/nomad/api"

	"ai-model-scheduler/internal/config"
	"ai-model-scheduler/internal/nomadapi"
)

// Env carries the environment facts a builder needs, derived from app config.
type Env struct {
	Driver           string // podman | docker
	ModelRootHost    string // NAS mount on the remote box, e.g. /mnt/models
	ModelMount       string // mount point inside containers, e.g. /models
	ImagePullTimeout string // e.g. "45m"; empty leaves the driver default (5m)
	Images           config.Images
}

// EnvFromConfig extracts builder inputs from the app configuration.
func EnvFromConfig(cfg config.Config) Env {
	return Env{
		Driver:           cfg.Driver,
		ModelRootHost:    cfg.ModelRootHost,
		ModelMount:       cfg.ModelMount,
		ImagePullTimeout: cfg.ImagePullTimeout,
		Images:           cfg.Images,
	}
}

// Params describes one model deployment.
type Params struct {
	Name      string `json:"name"`    // deployment name; job ID becomes model-<name>
	Runtime   string `json:"runtime"` // llamacpp | vllm | ollama
	Model     string `json:"model"`   // path relative to the model root
	Port      int    `json:"port"`
	GPU       string `json:"gpu"` // GPU vendor: "" (CPU) | nvidia | amd | intel
	CtxSize   int    `json:"ctx_size,omitempty"`
	ExtraArgs string `json:"extra_args,omitempty"` // whitespace-separated extra CLI args
	CPUMHz    int    `json:"cpu_mhz,omitempty"`
	MemMB     int    `json:"mem_mb,omitempty"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// Validate checks fields that are independent of cluster state.
func (p Params) Validate() error {
	if !nameRe.MatchString(p.Name) {
		return fmt.Errorf("name must match %s", nameRe)
	}
	switch p.Runtime {
	case "llamacpp", "vllm", "ollama":
	default:
		return fmt.Errorf("unknown runtime %q", p.Runtime)
	}
	switch p.GPU {
	case "", "nvidia", "amd", "intel":
	default:
		return fmt.Errorf("unknown gpu vendor %q (want nvidia, amd, intel, or empty for CPU)", p.GPU)
	}
	if p.Runtime != "ollama" && p.Model == "" {
		return fmt.Errorf("model is required")
	}
	return nil
}

// Build returns the Nomad job for the given deployment parameters.
func Build(p Params, env Env) (*api.Job, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	switch p.Runtime {
	case "llamacpp":
		return llamaCPP(p, env)
	case "vllm":
		return vLLM(p, env)
	case "ollama":
		return ollama(p, env)
	}
	return nil, fmt.Errorf("unknown runtime %q", p.Runtime)
}

// runtimeImage resolves the container image for a runtime and GPU vendor.
// A missing map key or empty value means the combination has no configured
// image; the error tells the user which override to set.
func runtimeImage(runtime, gpu string, byVendor map[string]string) (string, error) {
	if image := byVendor[gpu]; image != "" {
		return image, nil
	}
	label := gpu
	if label == "" {
		label = "CPU"
	}
	return "", fmt.Errorf("no %s image configured for %s; set the matching IMAGE_* variable", runtime, label)
}

// gpuDevices returns the podman/docker device passthrough for a GPU vendor:
// NVIDIA via its CDI spec, AMD ROCm via the kernel compute + DRM nodes,
// Intel via the DRM nodes.
func gpuDevices(vendor string) []string {
	switch vendor {
	case "nvidia":
		return []string{"nvidia.com/gpu=all"}
	case "amd":
		return []string{"/dev/kfd", "/dev/dri"}
	case "intel":
		return []string{"/dev/dri"}
	}
	return nil
}

// baseJob builds the shared service-job scaffolding: one group, one task,
// static port labeled nomadapi.PortLabel, model volume mounted read-only,
// management meta tags.
func baseJob(p Params, env Env, image string, args []string) *api.Job {
	jobID := nomadapi.JobPrefix + p.Name

	cpu := p.CPUMHz
	if cpu == 0 {
		cpu = 1000
	}
	mem := p.MemMB
	if mem == 0 {
		mem = 4096
	}

	task := &api.Task{
		Name:   nomadapi.TaskName,
		Driver: env.Driver,
		Config: map[string]any{
			"image":   image,
			"args":    args,
			"ports":   []string{nomadapi.PortLabel},
			"volumes": []string{fmt.Sprintf("%s:%s:ro", env.ModelRootHost, env.ModelMount)},
		},
		Resources: &api.Resources{
			CPU:      ptr(cpu),
			MemoryMB: ptr(mem),
		},
	}
	if devices := gpuDevices(p.GPU); devices != nil {
		task.Config["devices"] = devices
	}
	// Both the docker and podman drivers accept this key; their 5m default
	// is far too short for multi-GB CUDA/ROCm images.
	if env.ImagePullTimeout != "" {
		task.Config["image_pull_timeout"] = env.ImagePullTimeout
	}

	group := &api.TaskGroup{
		Name:  ptr("model"),
		Count: ptr(1),
		Networks: []*api.NetworkResource{{
			ReservedPorts: []api.Port{{
				Label: nomadapi.PortLabel,
				Value: p.Port,
				To:    p.Port,
			}},
		}},
		Tasks: []*api.Task{task},
	}

	return &api.Job{
		ID:          ptr(jobID),
		Name:        ptr(jobID),
		Type:        ptr(api.JobTypeService),
		Datacenters: []string{"*"},
		TaskGroups:  []*api.TaskGroup{group},
		Meta: map[string]string{
			nomadapi.ManagedByKey: nomadapi.ManagedByValue,
			nomadapi.KindKey:      nomadapi.KindInference,
			nomadapi.RuntimeKey:   p.Runtime,
			nomadapi.ModelKey:     p.Model,
			nomadapi.GPUKey:       p.GPU,
		},
	}
}

func ptr[T any](v T) *T { return &v }

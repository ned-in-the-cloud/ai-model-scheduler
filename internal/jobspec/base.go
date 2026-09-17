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
	Driver        string // podman | docker
	ModelRootHost string // NAS mount on the remote box, e.g. /mnt/models
	ModelMount    string // mount point inside containers, e.g. /models
	Images        config.Images
}

// EnvFromConfig extracts builder inputs from the app configuration.
func EnvFromConfig(cfg config.Config) Env {
	return Env{
		Driver:        cfg.Driver,
		ModelRootHost: cfg.ModelRootHost,
		ModelMount:    cfg.ModelMount,
		Images:        cfg.Images,
	}
}

// Params describes one model deployment.
type Params struct {
	Name      string `json:"name"`    // deployment name; job ID becomes model-<name>
	Runtime   string `json:"runtime"` // llamacpp | vllm | ollama
	Model     string `json:"model"`   // path relative to the model root
	Port      int    `json:"port"`
	GPU       bool   `json:"gpu"`
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
		return llamaCPP(p, env), nil
	case "vllm":
		return vLLM(p, env), nil
	case "ollama":
		return ollama(p, env), nil
	}
	return nil, fmt.Errorf("unknown runtime %q", p.Runtime)
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
	if p.GPU {
		task.Config["devices"] = []string{"nvidia.com/gpu=all"}
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
			nomadapi.GPUKey:       fmt.Sprintf("%t", p.GPU),
		},
	}
}

func ptr[T any](v T) *T { return &v }

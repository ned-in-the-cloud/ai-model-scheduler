package jobspec

import (
	"path"
	"strconv"
	"strings"

	"github.com/hashicorp/nomad/api"
)

// vLLM builds a vLLM OpenAI-compatible server job serving a Hugging Face
// model directory from the model share. vLLM requires a GPU in practice.
func vLLM(p Params, env Env) *api.Job {
	args := []string{
		"--model", path.Join(env.ModelMount, p.Model),
		"--host", "0.0.0.0",
		"--port", strconv.Itoa(p.Port),
		"--served-model-name", p.Name,
	}
	if p.CtxSize > 0 {
		args = append(args, "--max-model-len", strconv.Itoa(p.CtxSize))
	}
	if extra := strings.Fields(p.ExtraArgs); len(extra) > 0 {
		args = append(args, extra...)
	}

	job := baseJob(p, env, env.Images.VLLM, args)

	// vLLM needs a large /dev/shm for tensor-parallel workers. The docker
	// driver takes bytes; the podman driver takes a size string.
	task := job.TaskGroups[0].Tasks[0]
	if env.Driver == "docker" {
		task.Config["shm_size"] = 8 << 30
	} else {
		task.Config["shm_size"] = "8g"
	}
	return job
}

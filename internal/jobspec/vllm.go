package jobspec

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/hashicorp/nomad/api"
)

// vLLM builds a vLLM OpenAI-compatible server job serving a Hugging Face
// model directory from the model share. vLLM requires a GPU.
func vLLM(p Params, env Env) (*api.Job, error) {
	if p.GPU == "" {
		return nil, fmt.Errorf("vllm requires a GPU; select one on the deploy form")
	}
	image, err := runtimeImage("vLLM", p.GPU, map[string]string{
		"nvidia": env.Images.VLLM,
		"amd":    env.Images.VLLMROCm,
		"intel":  env.Images.VLLMIntel,
	})
	if err != nil {
		return nil, err
	}

	model := path.Join(env.ModelMount, p.Model)
	args := []string{
		"--model", model,
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

	job := baseJob(p, env, image, args)

	// vLLM needs a large /dev/shm for tensor-parallel workers. The docker
	// driver takes bytes; the podman driver takes a size string.
	task := job.TaskGroups[0].Tasks[0]
	if env.Driver == "docker" {
		task.Config["shm_size"] = 8 << 30
	} else {
		task.Config["shm_size"] = "8g"
	}

	// Run from the model directory so relative file paths in extra args
	// resolve against files shipped with the model, e.g. Nemotron's
	// --reasoning-parser-plugin nano_v3_reasoning_parser.py. The images'
	// own working directory (/app) is otherwise used.
	workDir := model
	if strings.HasSuffix(strings.ToLower(model), ".gguf") {
		workDir = path.Dir(model)
	}
	if env.Driver == "docker" {
		task.Config["work_dir"] = workDir
	} else {
		task.Config["working_dir"] = workDir
	}
	return job, nil
}

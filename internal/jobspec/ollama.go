package jobspec

import (
	"fmt"
	"path"

	"github.com/hashicorp/nomad/api"
)

// ollama builds an Ollama server job. Ollama manages its own model store in
// an "ollama" subdirectory of the model share, so the volume is mounted
// read-write; models are pulled via Ollama's own CLI/API. The default image
// bundles CUDA support, so CPU and NVIDIA share it; AMD uses the rocm tag.
func ollama(p Params, env Env) (*api.Job, error) {
	image, err := runtimeImage("Ollama", p.GPU, map[string]string{
		"":       env.Images.Ollama,
		"nvidia": env.Images.Ollama,
		"amd":    env.Images.OllamaROCm,
		"intel":  env.Images.OllamaIntel,
	})
	if err != nil {
		return nil, err
	}

	job := baseJob(p, env, image, nil)

	task := job.TaskGroups[0].Tasks[0]
	task.Config["volumes"] = []string{fmt.Sprintf("%s:%s", env.ModelRootHost, env.ModelMount)}
	if task.Env == nil {
		task.Env = map[string]string{}
	}
	// Defaults only — user-supplied env (already in task.Env) wins.
	for k, v := range map[string]string{
		"OLLAMA_MODELS": path.Join(env.ModelMount, "ollama"),
		"OLLAMA_HOST":   fmt.Sprintf("0.0.0.0:%d", p.Port),
	} {
		if _, ok := task.Env[k]; !ok {
			task.Env[k] = v
		}
	}
	return job, nil
}

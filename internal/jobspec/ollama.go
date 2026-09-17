package jobspec

import (
	"fmt"
	"path"

	"github.com/hashicorp/nomad/api"
)

// ollama builds an Ollama server job. Ollama manages its own model store in
// an "ollama" subdirectory of the model share, so the volume is mounted
// read-write; models are pulled via Ollama's own CLI/API.
func ollama(p Params, env Env) *api.Job {
	job := baseJob(p, env, env.Images.Ollama, nil)

	task := job.TaskGroups[0].Tasks[0]
	task.Config["volumes"] = []string{fmt.Sprintf("%s:%s", env.ModelRootHost, env.ModelMount)}
	task.Env = map[string]string{
		"OLLAMA_MODELS": path.Join(env.ModelMount, "ollama"),
		"OLLAMA_HOST":   fmt.Sprintf("0.0.0.0:%d", p.Port),
	}
	return job
}

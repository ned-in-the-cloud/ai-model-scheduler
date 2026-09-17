package jobspec

import (
	"path"
	"strconv"
	"strings"

	"github.com/hashicorp/nomad/api"
)

// llamaCPP builds a llama-server job serving a GGUF file from the model share.
func llamaCPP(p Params, env Env) (*api.Job, error) {
	image, err := runtimeImage("llama.cpp", p.GPU, map[string]string{
		"":       env.Images.LlamaCPP,
		"nvidia": env.Images.LlamaCPPCUDA,
		"amd":    env.Images.LlamaCPPROCm,
		"intel":  env.Images.LlamaCPPIntel,
	})
	if err != nil {
		return nil, err
	}

	args := []string{
		"-m", path.Join(env.ModelMount, p.Model),
		"--host", "0.0.0.0",
		"--port", strconv.Itoa(p.Port),
	}
	if p.CtxSize > 0 {
		args = append(args, "-c", strconv.Itoa(p.CtxSize))
	}
	if p.GPU != "" {
		args = append(args, "--n-gpu-layers", "999")
	}
	if extra := strings.Fields(p.ExtraArgs); len(extra) > 0 {
		args = append(args, extra...)
	}

	return baseJob(p, env, image, args), nil
}

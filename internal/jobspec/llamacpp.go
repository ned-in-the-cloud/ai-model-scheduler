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
	if p.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(p.Threads))
	}
	if p.GPU != "" {
		layers := 999 // all layers unless the user limits them
		if p.GPULayers > 0 {
			layers = p.GPULayers
		}
		args = append(args, "--n-gpu-layers", strconv.Itoa(layers))
	}
	extra := strings.Fields(p.ExtraArgs)
	if !hasFlag(extra, "--cache-ram", "-cram") {
		args = append(args, "--cache-ram", strconv.Itoa(llamaCacheRAM(memLimitMB(p))))
	}
	args = append(args, extra...)

	return baseJob(p, env, image, args), nil
}

// llamaCacheHeadroomMB is host memory kept for llama-server itself (runtime,
// GPU driver buffers, request state) when sizing its prompt cache.
const llamaCacheHeadroomMB = 3072

// llamaCacheRAM sizes llama-server's prompt cache, in MiB, to fit the task's
// memory limit. The cache lives in host RAM and defaults to 8 GiB, so under
// a smaller limit it grows with each request until the container is
// OOM-killed.
func llamaCacheRAM(memMB int) int {
	return min(8192, max(0, memMB-llamaCacheHeadroomMB))
}

// hasFlag reports whether args set any of the named flags, as "--flag v" or
// "--flag=v".
func hasFlag(args []string, names ...string) bool {
	for _, a := range args {
		for _, n := range names {
			if a == n || strings.HasPrefix(a, n+"=") {
				return true
			}
		}
	}
	return false
}

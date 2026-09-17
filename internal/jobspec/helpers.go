package jobspec

import (
	"fmt"

	"github.com/hashicorp/nomad/api"

	"ai-model-scheduler/internal/nomadapi"
)

// Helper job IDs. These are parameterized batch jobs the app self-registers
// at startup and dispatches on demand. They run on the remote box, which has
// the NAS share mounted, so the UI itself never needs filesystem access.
const (
	IndexerJobID    = "ams-indexer"
	HFDownloadJobID = "ams-hf-download"
)

// Indexer builds the parameterized batch job that scans the model share and
// prints one JSON object per model to stdout (NDJSON), which the app reads
// back through the allocation logs API.
func Indexer(env Env) *api.Job {
	// Paths containing double quotes are skipped rather than escaped; model
	// filenames on the share are expected to be sane.
	script := fmt.Sprintf(`set -e
M=%[1]s
find "$M" -type f -name '*.gguf' ! -path "$M/ollama/*" 2>/dev/null | while read -r f; do
  case "$f" in *\"*) continue;; esac
  sz=$(stat -c %%s "$f" 2>/dev/null || echo 0)
  printf '{"kind":"gguf","path":"%%s","size_bytes":%%s}\n' "${f#"$M"/}" "$sz"
done
find "$M" -type f -name 'config.json' ! -path "$M/ollama/*" 2>/dev/null | while read -r f; do
  case "$f" in *\"*) continue;; esac
  d=$(dirname "$f")
  sz=$(du -sk "$d" 2>/dev/null | cut -f1 || echo 0)
  printf '{"kind":"hf-dir","path":"%%s","size_bytes":%%s}\n' "${d#"$M"/}" "$((${sz:-0} * 1024))"
done
true`, env.ModelMount)

	return helperJob(IndexerJobID, env, env.Images.Indexer,
		[]string{"-c", script}, true /* read-only mount */, 200, 128)
}

// helperJob builds the shared scaffolding for parameterized batch helpers:
// sh -c task with the model share mounted.
func helperJob(id string, env Env, image string, shArgs []string, readOnly bool, cpu, mem int) *api.Job {
	volume := fmt.Sprintf("%s:%s", env.ModelRootHost, env.ModelMount)
	if readOnly {
		volume += ":ro"
	}
	task := &api.Task{
		Name:   nomadapi.TaskName,
		Driver: env.Driver,
		Config: map[string]any{
			"image":   image,
			"command": "/bin/sh",
			"args":    shArgs,
			"volumes": []string{volume},
		},
		Resources: &api.Resources{CPU: ptr(cpu), MemoryMB: ptr(mem)},
	}
	group := &api.TaskGroup{
		Name:  ptr("helper"),
		Count: ptr(1),
		Tasks: []*api.Task{task},
		// Batch helpers should fail fast, not retry forever.
		RestartPolicy: &api.RestartPolicy{Attempts: ptr(0), Mode: ptr("fail")},
	}
	return &api.Job{
		ID:               ptr(id),
		Name:             ptr(id),
		Type:             ptr(api.JobTypeBatch),
		Datacenters:      []string{"*"},
		ParameterizedJob: &api.ParameterizedJobConfig{},
		TaskGroups:       []*api.TaskGroup{group},
		Meta: map[string]string{
			nomadapi.ManagedByKey: nomadapi.ManagedByValue,
			nomadapi.KindKey:      nomadapi.KindHelper,
		},
	}
}

package jobspec

import (
	"fmt"
	"strings"

	"github.com/hashicorp/nomad/api"

	"ai-model-scheduler/internal/nomadapi"
)

// escapeInterpolation escapes shell ${var} expansions so Nomad's HCL2
// config parsing passes them through literally. Only the exact sequence ${
// starts an HCL2 interpolation ($( and bare $NAME are left alone), and only
// $${ is its escape form.
func escapeInterpolation(script string) string {
	return strings.ReplaceAll(script, "${", "$${")
}

// Helper job IDs. These are parameterized batch jobs the app self-registers
// at startup and dispatches on demand. They run on the remote box, which has
// the NAS share mounted, so the UI itself never needs filesystem access.
const (
	IndexerJobID    = "ams-indexer"
	HFDownloadJobID = "ams-hf-download"
	GPUProbeJobID   = "ams-gpu-probe"
	GPUStatsJobID   = "ams-gpu-stats"
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
		[]string{"-c", escapeInterpolation(script)}, true /* read-only mount */, 200, 128)
}

// GPUProbe builds the batch job that detects GPUs on the box. It enumerates
// PCI display-class devices via sysfs (visible inside containers and
// independent of which GPU drivers are loaded, so a freshly swapped-in card
// still shows up), maps vendor IDs to names, and prints one JSON object per
// GPU to stdout. Friendly names come from the NVIDIA procfs when present,
// then best-effort lspci; the fallback is the PCI vendor:device ID.
func GPUProbe(env Env) *api.Job {
	script := `apk add -q --no-cache pciutils 2>/dev/null || true
i=0
for d in /sys/bus/pci/devices/*; do
  class=$(cat "$d/class" 2>/dev/null) || continue
  case "$class" in 0x03*) ;; *) continue;; esac
  ven=$(cat "$d/vendor"); dev=$(cat "$d/device"); pci=$(basename "$d")
  case "$ven" in
    0x10de) vendor=nvidia;;
    0x1002) vendor=amd;;
    0x8086) vendor=intel;;
    *) vendor=other;;
  esac
  name=""
  if [ "$vendor" = nvidia ] && [ -r "/proc/driver/nvidia/gpus/$pci/information" ]; then
    name=$(sed -n 's/^Model:[[:space:]]*//p' "/proc/driver/nvidia/gpus/$pci/information" | head -n1)
  fi
  if [ -z "$name" ] && command -v lspci >/dev/null 2>&1; then
    name=$(lspci -mm -s "$pci" 2>/dev/null | awk -F'"' '{print $4" "$6}')
  fi
  [ -z "$name" ] && name="$vendor device ${ven#0x}:${dev#0x}"
  name=$(printf '%s' "$name" | tr -d '"\\')
  printf '{"index":%s,"vendor":"%s","name":"%s","pci":"%s"}\n' "$i" "$vendor" "$name" "$pci"
  i=$((i+1))
done
true`

	return helperJob(GPUProbeJobID, env, env.Images.Indexer,
		[]string{"-c", escapeInterpolation(script)}, true /* no writes needed */, 200, 128)
}

// GPUStatsAgent builds the long-running service job that samples GPU
// utilization every few seconds and prints one JSON line per sample to
// stdout; the app reads the latest sample back through the allocation logs
// API, so no extra ports or host access are needed. AMD stats come from
// amdgpu's sysfs (readable without ROCm or device access); NVIDIA stats come
// from nvidia-smi, which the CDI device injects into the container — so the
// CDI device is attached only when an NVIDIA GPU is present.
func GPUStatsAgent(env Env, nvidia bool) *api.Job {
	// Sentinels in the emitted JSON: -1 = unknown for percentages/temp/power,
	// 0 = unknown for VRAM byte counts.
	script := `while true; do
  ts=$(date +%s)
  gpus=""
  for d in /sys/class/drm/card*/device; do
    [ -e "$d/vendor" ] || continue
    [ "$(cat "$d/vendor")" = "0x1002" ] || continue
    pci=$(basename "$(readlink -f "$d")")
    busy=$(cat "$d/gpu_busy_percent" 2>/dev/null) || busy=-1
    vu=$(cat "$d/mem_info_vram_used" 2>/dev/null) || vu=0
    vt=$(cat "$d/mem_info_vram_total" 2>/dev/null) || vt=0
    temp=-1
    tf=$(ls "$d"/hwmon/hwmon*/temp1_input 2>/dev/null | head -n1)
    [ -n "$tf" ] && temp=$(( $(cat "$tf") / 1000 ))
    pw=-1
    pf=$(ls "$d"/hwmon/hwmon*/power1_average 2>/dev/null | head -n1)
    [ -z "$pf" ] && pf=$(ls "$d"/hwmon/hwmon*/power1_input 2>/dev/null | head -n1)
    [ -n "$pf" ] && pw=$(( $(cat "$pf") / 1000000 ))
    g=$(printf '{"vendor":"amd","pci":"%s","util_percent":%s,"vram_used_bytes":%s,"vram_total_bytes":%s,"temp_c":%s,"power_w":%s}' "$pci" "$busy" "$vu" "$vt" "$temp" "$pw")
    gpus="${gpus:+$gpus,}$g"
  done
  if command -v nvidia-smi >/dev/null 2>&1; then
    nvidia-smi --query-gpu=name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw --format=csv,noheader,nounits 2>/dev/null | sed 's/, /,/g' > /tmp/nv.csv || true
    while IFS=, read -r name util mu mt temp pw; do
      [ -n "$name" ] || continue
      case "$util" in ''|*[!0-9]*) util=-1;; esac
      case "$mu" in ''|*[!0-9]*) mu=0;; esac
      case "$mt" in ''|*[!0-9]*) mt=0;; esac
      case "$temp" in ''|*[!0-9]*) temp=-1;; esac
      case "$pw" in ''|*[!0-9.]*) pw=-1;; esac
      name=$(printf '%s' "$name" | tr -d '"\\')
      g=$(printf '{"vendor":"nvidia","name":"%s","util_percent":%s,"vram_used_bytes":%s,"vram_total_bytes":%s,"temp_c":%s,"power_w":%s}' "$name" "$util" "$((mu*1048576))" "$((mt*1048576))" "$temp" "$pw")
      gpus="${gpus:+$gpus,}$g"
    done < /tmp/nv.csv
  fi
  printf '{"ts":%s,"gpus":[%s]}\n' "$ts" "$gpus"
  sleep 5
done`

	task := &api.Task{
		Name:   nomadapi.TaskName,
		Driver: env.Driver,
		Config: map[string]any{
			"image":   env.Images.GPUStats,
			"command": "/bin/sh",
			"args":    []string{"-c", escapeInterpolation(script)},
		},
		Resources: &api.Resources{CPU: ptr(100), MemoryMB: ptr(128)},
	}
	if nvidia {
		task.Config["devices"] = []string{"nvidia.com/gpu=all"}
	}
	if env.ImagePullTimeout != "" {
		task.Config["image_pull_timeout"] = env.ImagePullTimeout
	}

	group := &api.TaskGroup{
		Name:  ptr("agent"),
		Count: ptr(1),
		Tasks: []*api.Task{task},
		// Keep retrying quietly if the box hiccups rather than failing dead.
		RestartPolicy: &api.RestartPolicy{Mode: ptr(api.RestartPolicyModeDelay)},
	}
	return &api.Job{
		ID:          ptr(GPUStatsJobID),
		Name:        ptr(GPUStatsJobID),
		Type:        ptr(api.JobTypeService),
		Datacenters: []string{"*"},
		TaskGroups:  []*api.TaskGroup{group},
		Meta: map[string]string{
			nomadapi.ManagedByKey: nomadapi.ManagedByValue,
			nomadapi.KindKey:      nomadapi.KindHelper,
		},
	}
}

// HFDownload builds the parameterized batch job that downloads a Hugging
// Face repo into the model share. Dispatch meta: repo_id and dest (required),
// revision and include (optional). Nomad exposes dispatch meta to the task as
// NOMAD_META_* environment variables.
func HFDownload(env Env, hfToken string) *api.Job {
	script := fmt.Sprintf(`set -e
export PIP_ROOT_USER_ACTION=ignore
pip install --quiet 'huggingface_hub>=0.34'
echo "downloading $NOMAD_META_repo_id -> %[1]s/$NOMAD_META_dest"
hf download "$NOMAD_META_repo_id" \
  ${NOMAD_META_revision:+--revision "$NOMAD_META_revision"} \
  ${NOMAD_META_include:+--include "$NOMAD_META_include"} \
  --max-workers 2 \
  --local-dir "%[1]s/$NOMAD_META_dest"
echo "download complete"`, env.ModelMount)

	// Memory must cover the page cache for buffered writes to the share
	// (cgroup v2 charges it to the task), not just the Python process —
	// too small a limit OOM-kills large downloads mid-transfer.
	job := helperJob(HFDownloadJobID, env, env.Images.HFDownload,
		[]string{"-c", escapeInterpolation(script)}, false /* needs write access */, 500, 4096)
	job.ParameterizedJob.MetaRequired = []string{"repo_id", "dest"}
	job.ParameterizedJob.MetaOptional = []string{"revision", "include"}
	if hfToken != "" {
		job.TaskGroups[0].Tasks[0].Env = map[string]string{"HF_TOKEN": hfToken}
	}
	return job
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
	if env.ImagePullTimeout != "" {
		task.Config["image_pull_timeout"] = env.ImagePullTimeout
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

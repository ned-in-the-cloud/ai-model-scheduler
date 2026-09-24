# AI Model Scheduler

A small web UI for deploying and managing LLM inference workloads on a remote
Linux box via [HashiCorp Nomad](https://www.nomadproject.io/). Models live on
a NAS share mounted on the box; containers run under podman.

- **Deploy / stop models** — llama.cpp (GGUF), vLLM (HF dirs, GPU), Ollama
- **GPU selection** — a dispatched probe job detects GPUs on the box (NVIDIA,
  AMD, Intel); picking one selects the matching runtime image (CUDA/ROCm/SYCL)
  and device passthrough automatically
- **Dashboard** — running models, health, endpoints, live CPU/RSS usage, node
  stats, and per-GPU utilization/VRAM/temperature/power (via a small
  `ams-gpu-stats` service job the app runs on the box: AMD from sysfs,
  NVIDIA from nvidia-smi)
- **Model catalog** — browses the NAS share via a dispatched indexer job
- **Hugging Face downloads** — dispatched batch jobs pull models onto the share
- **Benchmarks** — define a suite of model configurations (form or YAML),
  run them one at a time against a load test and an accuracy eval (your own
  JSONL set or lm-eval-harness tasks), and compare the results side by side
- Single Go binary, htmx frontend, multi-arch container (amd64 + arm64)

The UI is a **pure Nomad API client**: it needs no SSH, no NAS mount, no agent
on the box. File browsing and downloads run as parameterized batch jobs on the
box itself (self-registered at first use, read back through the allocation
logs API). The only required setting is `NOMAD_ADDR`.

## Quick start

1. Prepare the remote box once: [`deploy/remote-box-setup.md`](deploy/remote-box-setup.md)
2. Run the UI (Windows box or Raspberry Pi):

```bash
docker build -t ai-model-scheduler .
docker run -d -p 8080:8080 -v scheduler-data:/data -e NOMAD_ADDR=http://<box-ip>:4646 ai-model-scheduler
# or: docker compose -f deploy/compose.example.yml up -d
```

3. Open http://localhost:8080 — the Nomad badge in the top bar should be green.

## Configuration

Environment variables (or a YAML file via `CONFIG_FILE`; env wins):

| Variable | Default | Purpose |
|---|---|---|
| `NOMAD_ADDR` | `http://127.0.0.1:4646` | Nomad API address |
| `NOMAD_TOKEN` | – | ACL token, if ACLs are enabled |
| `NOMAD_DRIVER` | `podman` | Task driver (`docker` for local dev) |
| `IMAGE_PULL_TIMEOUT` | `45m` | Max time to pull a task's image (driver default is 5m — too short for GPU images) |
| `MODEL_ROOT_HOST` | `/mnt/models` | NAS mount path on the box |
| `HF_TOKEN` | – | Hugging Face token for gated repos |
| `LISTEN_ADDR` | `:8080` | UI listen address |
| `BASIC_AUTH_USER` / `BASIC_AUTH_PASS` | – | Enable basic auth when both set |
| `PORT_MIN` / `PORT_MAX` | `8000` / `8999` | Endpoint port range |
| `CATALOG_TTL` | `5m` | Model catalog cache lifetime |
| `IMAGE_LLAMACPP` | `ghcr.io/ggml-org/llama.cpp:server` | llama.cpp CPU image |
| `IMAGE_LLAMACPP_CUDA` | `ghcr.io/ggml-org/llama.cpp:server-cuda` | llama.cpp NVIDIA image |
| `IMAGE_LLAMACPP_ROCM` | `ghcr.io/ggml-org/llama.cpp:server-rocm` | llama.cpp AMD image |
| `IMAGE_LLAMACPP_INTEL` | `ghcr.io/ggml-org/llama.cpp:server-intel` | llama.cpp Intel (SYCL) image |
| `IMAGE_VLLM` | `docker.io/vllm/vllm-openai:latest` | vLLM NVIDIA image |
| `IMAGE_VLLM_ROCM` | `docker.io/vllm/vllm-openai-rocm:latest` | vLLM AMD image |
| `IMAGE_VLLM_INTEL` | – | vLLM Intel image (unset = unsupported) |
| `IMAGE_OLLAMA` | `docker.io/ollama/ollama:latest` | Ollama CPU/NVIDIA image |
| `IMAGE_OLLAMA_ROCM` | `docker.io/ollama/ollama:rocm` | Ollama AMD image |
| `IMAGE_OLLAMA_INTEL` | – | Ollama Intel image (unset = unsupported) |
| `IMAGE_GPUSTATS` | `docker.io/library/debian:bookworm-slim` | GPU stats agent image (glibc required for nvidia-smi) |
| `IMAGE_BENCH` | `ghcr.io/ned-in-the-cloud/ai-model-scheduler-bench:latest` | Benchmark eval image (built from `bench/`) |
| `DATA_DIR` | `/data` in the container (`data` otherwise) | Benchmark suites, datasets, and results; mount a volume |

A JSON API mirrors everything the UI does under `/api/v1/` (deployments,
models, downloads, stats, hardware, health). Deployments take a `gpu` field
holding the vendor (`nvidia`, `amd`, `intel`) or empty for CPU;
`GET /api/v1/hardware/gpus` lists what the probe detected.

## Benchmarks

The Benchmarks page compares model configurations. A **suite** is a named
list of configs (the deploy form's fields plus a label), created in the UI
or uploaded as YAML:

```yaml
name: qwen-vllm-sweep
configs:
  - label: fp16-8k
    runtime: vllm
    model: Qwen/Qwen2.5-7B-Instruct
    gpu: nvidia
    ctx_size: 8192
    extra_args: "--gpu-memory-utilization 0.9"
  - label: awq-8k
    runtime: vllm
    model: Qwen/Qwen2.5-7B-Instruct-AWQ
    gpu: nvidia
    ctx_size: 8192
    extra_args: "--quantization awq"
```

A **run** deploys each config in series: stop everything else that is
running, deploy the config, wait for it to answer, dispatch an `ams-bench-*`
batch job on the box that measures it, tear it down, and finally relaunch
the deployments it stopped. A config that fails to start or errors out is
marked failed and the run moves on. Runs survive page reloads; if the app
restarts mid-run, the run is marked interrupted and the prior deployments
are relaunched on startup.

Each config is measured for:

- **Performance** — single-stream decode tokens/s and time to first token,
  then aggregate tokens/s and latency at each concurrency level (default
  1, 4, 8), plus time to first response and peak VRAM / average GPU
  utilization sampled from the stats agent.
- **Accuracy** (optional) — a JSONL dataset (`{"prompt": ..., "expected":
  ...}` per line, scored by normalized exact match; upload up to 512 KB or
  point at a larger file on the share), or lm-eval-harness tasks from a
  curated list with a per-task sample limit. Harness datasets are cached on
  the share under `_benchmarks/.hf-cache`.

Results appear as a side-by-side table (best value per row highlighted) with
CSV and JSON export, and a copy of each config's raw result is written to
`_benchmarks/<run>/<index>/result.json` on the share. Suites, datasets, and
results live in `DATA_DIR`; mount a volume there.

The eval job runs the `bench/` image (Python load generator plus
lm-eval-harness), published to GHCR by `.github/workflows/bench-image.yml`.
The box needs to pull it, and lm-eval tasks need internet access on the box
for the first download of each dataset.

## Development

```bash
make test          # unit tests (no cluster needed)
make build         # bin/scheduler
make dev-nomad     # local `nomad agent -dev` in Docker for integration testing
NOMAD_DRIVER=docker NOMAD_ADDR=http://localhost:4646 make run
make image-multiarch   # buildx amd64+arm64
```

The task driver is config-driven so the same code path runs against a local
dev agent with the docker driver and the real box with podman.

## Smoke checklist (real box)

1. **Connectivity** — start the UI, Nomad badge green, node stats populate.
2. **Deploy** — Deploy page → pick a GGUF from the share → llama.cpp, CPU →
   endpoint appears → `curl http://<endpoint>/v1/models` responds → Stop.
3. **Catalog** — Models page lists the share; Refresh rescans.
4. **Download** — Downloads page → small public repo (e.g. a <1 GB GGUF with
   an `--include` pattern) → status reaches complete → model appears in catalog.
5. **GPU** — deploy llama.cpp with GPU on → `nvidia-smi` on the box shows the
   process; vLLM with an HF dir → OpenAI-compatible completion succeeds;
   Ollama → `curl http://<endpoint>/api/tags` answers.

#!/usr/bin/env python3
"""Benchmark one OpenAI-compatible model endpoint.

Runs inside the ams-bench-* Nomad batch job. Configuration arrives through
BENCH_* environment variables; results go to stdout as a single "RESULT
<json>" line (the app reads it back through Nomad's logs API) plus a copy in
BENCH_OUT_DIR on the model share. "PROGRESS <text>" lines report status
while working.

Measurements:
  * readiness: time until the endpoint answers a warm-up request
  * single-stream: sequential streamed requests -> decode tok/s, TTFT p50/p95
  * concurrency sweep: N parallel requests -> aggregate tok/s, latency p50/p95
  * accuracy: a JSONL prompt/expected set (normalized exact match) or
    lm-eval-harness tasks via its local-chat-completions backend
  * token speed: a JSONL set's prompts replayed one at a time, measuring
    prefill and decode rates without scoring the responses

With BENCH_MODE=stage it instead copies BENCH_STAGE_SRC to BENCH_STAGE_DEST:
the app uses that to put uploaded datasets on the model share in parts.
"""

import base64
import glob
import gzip
import hashlib
import json
import os
import shutil
import re
import statistics
import subprocess
import sys
import time
import unicodedata
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor

ENDPOINT = os.environ.get("BENCH_ENDPOINT", "http://127.0.0.1:8000").rstrip("/")
MODEL = os.environ.get("BENCH_MODEL", "")
ACCURACY = os.environ.get("BENCH_ACCURACY", "none")
TASKS = [t for t in os.environ.get("BENCH_TASKS", "").split(",") if t]
LIMIT = int(os.environ.get("BENCH_LIMIT", "0") or 0)
CONCURRENCY = [int(n) for n in os.environ.get("BENCH_CONCURRENCY", "1,4,8").split(",") if n]
PROMPTS = int(os.environ.get("BENCH_PROMPTS", "8") or 8)
MAX_TOKENS = int(os.environ.get("BENCH_MAX_TOKENS", "256") or 256)
READY_TIMEOUT = int(os.environ.get("BENCH_READY_TIMEOUT", "900") or 900)
OUT_DIR = os.environ.get("BENCH_OUT_DIR", "")
DATASET = os.environ.get("BENCH_DATASET", "")
DATASET_PARTS = os.environ.get("BENCH_DATASET_PARTS", "")
DATASET_SHA256 = os.environ.get("BENCH_DATASET_SHA256", "")

REQUEST_TIMEOUT = 600  # seconds per request; long generations on slow GPUs

PERF_PROMPTS = [
    "Explain how a hash table works and when you would choose one over a balanced tree.",
    "Write a short story, about three paragraphs, about a lighthouse keeper who discovers a message in a bottle.",
    "Summarize the causes and consequences of the 2008 financial crisis for a high-school audience.",
    "Describe, step by step, how to make a sourdough starter from scratch.",
    "Compare TCP and UDP. Give concrete examples of applications suited to each.",
    "Write a Python function that merges two sorted lists into one sorted list, and explain its complexity.",
    "What are the main arguments for and against nuclear power as a climate solution?",
    "Explain the difference between weather and climate, with examples a ten-year-old would understand.",
    "Draft a polite email declining a job offer while keeping the door open for the future.",
    "Describe how a bill becomes a law in a typical parliamentary democracy.",
    "Give a tour of the solar system, one or two sentences per planet.",
    "Explain what a Docker container is and how it differs from a virtual machine.",
]

errors = []


def progress(msg):
    print("PROGRESS " + msg, flush=True)


def log(msg):
    print(msg, flush=True)


def http_json(path, body=None, timeout=REQUEST_TIMEOUT):
    url = ENDPOINT + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read().decode())


def percentile(values, pct):
    if not values:
        return 0.0
    values = sorted(values)
    k = (len(values) - 1) * pct / 100.0
    lo, hi = int(k), min(int(k) + 1, len(values) - 1)
    return values[lo] + (values[hi] - values[lo]) * (k - lo)


# --- readiness -------------------------------------------------------------

def wait_ready():
    """Poll /v1/models, then a warm-up completion. Returns (model, seconds)."""
    global MODEL
    start = time.time()
    deadline = start + READY_TIMEOUT
    last_err = ""
    while time.time() < deadline:
        try:
            models = http_json("/v1/models", timeout=10)
            ids = [m.get("id") for m in models.get("data", []) if m.get("id")]
            if ids:
                if MODEL not in ids:
                    MODEL = ids[0]
                http_json("/v1/chat/completions", {
                    "model": MODEL,
                    "messages": [{"role": "user", "content": "Say hello."}],
                    "max_tokens": 8,
                    "temperature": 0,
                }, timeout=120)
                return MODEL, time.time() - start
            last_err = "no models listed"
        except Exception as e:  # noqa: BLE001 - any failure means "not yet"
            last_err = str(e)
        time.sleep(3)
    raise SystemExit(f"endpoint not ready after {READY_TIMEOUT}s: {last_err}")


# --- streamed request ----------------------------------------------------

def chat_stream(prompt, max_tokens, messages=None, temperature=0.7):
    """One streamed chat completion. Returns timing + token counts."""
    body = {
        "model": MODEL,
        "messages": messages or [{"role": "user", "content": prompt}],
        "max_tokens": max_tokens,
        "temperature": temperature,
        "stream": True,
        "stream_options": {"include_usage": True},
    }
    req = urllib.request.Request(ENDPOINT + "/v1/chat/completions",
                                 data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json"})
    t0 = time.time()
    first = None
    chunks = 0
    usage_tokens = None
    prompt_tokens = None
    with urllib.request.urlopen(req, timeout=REQUEST_TIMEOUT) as resp:
        for raw in resp:
            line = raw.decode("utf-8", "replace").strip()
            if not line.startswith("data:"):
                continue
            payload = line[5:].strip()
            if payload == "[DONE]":
                break
            try:
                obj = json.loads(payload)
            except json.JSONDecodeError:
                continue
            usage = obj.get("usage")
            if usage and usage.get("completion_tokens") is not None:
                usage_tokens = usage["completion_tokens"]
            if usage and usage.get("prompt_tokens") is not None:
                prompt_tokens = usage["prompt_tokens"]
            for choice in obj.get("choices") or []:
                delta = choice.get("delta") or {}
                if delta.get("content"):
                    if first is None:
                        first = time.time()
                    chunks += 1
    end = time.time()
    if first is None:
        first = end
    tokens = usage_tokens if usage_tokens is not None else chunks
    return {"ttft": first - t0, "total": end - t0, "decode": end - first, "tokens": tokens,
            "prompt_tokens": prompt_tokens}


def prompt_list(n):
    return [PERF_PROMPTS[i % len(PERF_PROMPTS)] for i in range(n)]


def single_stream():
    progress("single-stream throughput")
    results, errs = [], 0
    for i, p in enumerate(prompt_list(PROMPTS)):
        try:
            results.append(chat_stream(p, MAX_TOKENS))
        except Exception as e:  # noqa: BLE001
            errs += 1
            errors.append(f"single-stream request {i}: {e}")
    if not results:
        return {"tok_per_sec": 0, "ttft_ms_p50": 0, "ttft_ms_p95": 0, "requests": 0, "errors": errs}
    tokens = sum(r["tokens"] for r in results)
    decode = sum(r["decode"] for r in results)
    ttfts = [r["ttft"] * 1000 for r in results]
    return {
        "tok_per_sec": round(tokens / decode, 2) if decode > 0 else 0,
        "ttft_ms_p50": round(percentile(ttfts, 50), 1),
        "ttft_ms_p95": round(percentile(ttfts, 95), 1),
        "requests": len(results),
        "errors": errs,
    }


def concurrency_level(n):
    progress(f"load test at concurrency {n}")
    count = max(PROMPTS, n)
    prompts = prompt_list(count)
    t0 = time.time()
    results, errs = [], 0
    with ThreadPoolExecutor(max_workers=n) as pool:
        for i, fut in enumerate([pool.submit(chat_stream, p, MAX_TOKENS) for p in prompts]):
            try:
                results.append(fut.result())
            except Exception as e:  # noqa: BLE001
                errs += 1
                errors.append(f"concurrency {n} request {i}: {e}")
    wall = time.time() - t0
    tokens = sum(r["tokens"] for r in results)
    lat = [r["total"] * 1000 for r in results]
    return {
        "n": n,
        "tok_per_sec": round(tokens / wall, 2) if wall > 0 else 0,
        "latency_ms_p50": round(percentile(lat, 50), 1),
        "latency_ms_p95": round(percentile(lat, 95), 1),
        "requests": len(results),
        "errors": errs,
    }


# --- accuracy: JSONL ---------------------------------------------------------

def normalize(s):
    s = unicodedata.normalize("NFKC", s or "").strip().lower()
    s = re.sub(r"\s+", " ", s)
    return s.strip(" .")


def load_rows(path):
    rows = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                rows.append(json.loads(line))
    return rows


def row_messages(row):
    messages = []
    if row.get("system"):
        messages.append({"role": "system", "content": row["system"]})
    messages.append({"role": "user", "content": row["prompt"]})
    return messages


def eval_jsonl(path):
    rows = load_rows(path)
    correct, samples = 0, []
    max_tokens = max(MAX_TOKENS, 256)
    for i, row in enumerate(rows):
        if i % 10 == 0:
            progress(f"jsonl eval {i}/{len(rows)}")
        try:
            resp = http_json("/v1/chat/completions", {
                "model": MODEL, "messages": row_messages(row),
                "max_tokens": max_tokens, "temperature": 0,
            })
            text = (resp.get("choices") or [{}])[0].get("message", {}).get("content") or ""
        except Exception as e:  # noqa: BLE001
            errors.append(f"jsonl row {i}: {e}")
            text = ""
        ok = normalize(text) == normalize(row["expected"])
        correct += ok
        if len(samples) < 200:
            samples.append({"prompt": row["prompt"][:500], "expected": row["expected"][:500],
                            "response": text[:500], "correct": ok})
    total = len(rows)
    return {"kind": "jsonl", "score": round(correct / total, 4) if total else 0,
            "correct": correct, "total": total, "samples": samples}


# --- token speed: JSONL replay, unscored -------------------------------------

def eval_tokens(path, limit):
    """Replay dataset prompts sequentially, measuring prefill and decode.

    Prefill rate is prompt tokens over time-to-first-token (which includes
    request overhead, so short prompts understate it); decode rate is
    completion tokens over the rest of the response.
    """
    rows = load_rows(path)
    if limit > 0:
        rows = rows[:limit]
    results, errs = [], 0
    for i, row in enumerate(rows):
        if i % 10 == 0:
            progress(f"token speed {i}/{len(rows)}")
        try:
            results.append(chat_stream(None, MAX_TOKENS, messages=row_messages(row), temperature=0))
        except Exception as e:  # noqa: BLE001
            errs += 1
            errors.append(f"token speed row {i}: {e}")
    if not results:
        return {"prefill_tok_per_sec": 0, "decode_tok_per_sec": 0, "ttft_ms_p50": 0, "ttft_ms_p95": 0,
                "prompt_tokens": 0, "completion_tokens": 0, "requests": 0, "errors": errs}
    if any(r["prompt_tokens"] is None for r in results):
        errors.append("server did not report prompt token usage; prefill rate unavailable")
    prompt_tokens = sum(r["prompt_tokens"] or 0 for r in results)
    completion_tokens = sum(r["tokens"] for r in results)
    ttft = sum(r["ttft"] for r in results)
    decode = sum(r["decode"] for r in results)
    ttfts = [r["ttft"] * 1000 for r in results]
    return {
        "prefill_tok_per_sec": round(prompt_tokens / ttft, 2) if ttft > 0 else 0,
        "decode_tok_per_sec": round(completion_tokens / decode, 2) if decode > 0 else 0,
        "ttft_ms_p50": round(percentile(ttfts, 50), 1),
        "ttft_ms_p95": round(percentile(ttfts, 95), 1),
        "prompt_tokens": prompt_tokens,
        "completion_tokens": completion_tokens,
        "requests": len(results),
        "errors": errs,
    }


# --- dataset staging -----------------------------------------------------------

def stage():
    """BENCH_MODE=stage: copy one staged part onto the share."""
    src, dest = os.environ["BENCH_STAGE_SRC"], os.environ["BENCH_STAGE_DEST"]
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    shutil.copyfile(src, dest)
    log(f"staged {os.path.getsize(dest)} bytes to {dest}")


def assemble_dataset(parts_dir, want_sha):
    """Rebuild an uploaded dataset from its staged gzip+base64 parts."""
    parts = sorted(glob.glob(os.path.join(parts_dir, "part-*")))
    if not parts:
        raise RuntimeError(f"no staged dataset parts in {parts_dir}")
    encoded = "".join(open(p, encoding="ascii").read().strip() for p in parts)
    data = gzip.decompress(base64.b64decode(encoded))
    if want_sha and hashlib.sha256(data).hexdigest() != want_sha:
        raise RuntimeError("staged dataset checksum mismatch")
    out = "/tmp/dataset.jsonl"
    with open(out, "wb") as f:
        f.write(data)
    return out


# --- accuracy: lm-eval-harness --------------------------------------------

PREFERRED_METRICS = [
    "exact_match,strict-match", "exact_match,flexible-extract", "exact_match,none",
    "prompt_level_strict_acc,none", "acc,none", "acc_norm,none", "bleu_acc,none",
]


def headline_metric(task_result):
    for key in PREFERRED_METRICS:
        if key in task_result and isinstance(task_result[key], (int, float)):
            return key.split(",")[0], float(task_result[key])
    for key, val in task_result.items():
        if key.endswith("_stderr") or key in ("alias",) or "stderr" in key:
            continue
        if isinstance(val, (int, float)):
            return key.split(",")[0], float(val)
    return "", 0.0


def eval_lmeval(tasks, limit):
    progress("lm-eval-harness: " + ",".join(tasks))
    out = "/tmp/lmeval"
    cmd = [
        sys.executable, "-m", "lm_eval",
        "--model", "local-chat-completions",
        "--model_args", f"model={MODEL},base_url={ENDPOINT}/v1/chat/completions,"
                        "num_concurrent=4,max_retries=3,tokenized_requests=False",
        "--tasks", ",".join(tasks),
        "--apply_chat_template",
        "--output_path", out,
        "--batch_size", "1",
    ]
    if limit > 0:
        cmd += ["--limit", str(limit)]
    log("running: " + " ".join(cmd))
    proc = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    tail = "\n".join(proc.stdout.splitlines()[-40:])
    log(tail)
    if proc.returncode != 0:
        raise RuntimeError(f"lm_eval exited {proc.returncode}: {tail.splitlines()[-1] if tail else ''}")
    files = sorted(glob.glob(os.path.join(out, "**", "results_*.json"), recursive=True))
    if not files:
        raise RuntimeError("lm_eval produced no results file")
    with open(files[-1], encoding="utf-8") as f:
        results = json.load(f).get("results", {})
    task_scores = {}
    for name, res in results.items():
        metric, value = headline_metric(res)
        if metric:
            task_scores[name] = {"metric": metric, "value": round(value, 4)}
    # Only the requested tasks count toward the overall score; groups report
    # subtasks too, which would double count.
    top = [task_scores[t]["value"] for t in tasks if t in task_scores]
    score = statistics.mean(top) if top else statistics.mean(v["value"] for v in task_scores.values()) if task_scores else 0
    return {"kind": "lmeval", "score": round(score, 4), "tasks": task_scores}


# --- main --------------------------------------------------------------------

def main():
    if os.environ.get("BENCH_MODE") == "stage":
        stage()
        return

    dataset = DATASET
    if DATASET_PARTS:
        # Fail fast: a bad staged copy would otherwise surface only after
        # the whole performance pass.
        dataset = assemble_dataset(DATASET_PARTS, DATASET_SHA256)

    model, wait_sec = wait_ready()
    log(f"endpoint ready in {wait_sec:.1f}s, model={model}")
    result = {"wait_sec": round(wait_sec, 1)}

    result["single"] = single_stream()
    result["concurrency"] = [concurrency_level(n) for n in CONCURRENCY]

    if ACCURACY in ("jsonl", "tokens") and (not dataset or not os.path.exists(dataset)):
        errors.append(f"dataset not found: {dataset}")
    elif ACCURACY == "jsonl":
        try:
            result["accuracy"] = eval_jsonl(dataset)
        except Exception as e:  # noqa: BLE001
            errors.append(f"jsonl eval failed: {e}")
    elif ACCURACY == "tokens":
        try:
            result["tokens"] = eval_tokens(dataset, LIMIT)
        except Exception as e:  # noqa: BLE001
            errors.append(f"token speed eval failed: {e}")
    elif ACCURACY == "lmeval":
        try:
            result["accuracy"] = eval_lmeval(TASKS, LIMIT)
        except Exception as e:  # noqa: BLE001
            errors.append(f"lm-eval failed: {e}")

    if errors:
        result["errors"] = errors[:50]

    if OUT_DIR:
        try:
            os.makedirs(OUT_DIR, exist_ok=True)
            with open(os.path.join(OUT_DIR, "result.json"), "w", encoding="utf-8") as f:
                json.dump(result, f, indent=2)
        except OSError as e:
            log(f"could not write result copy to {OUT_DIR}: {e}")

    progress("done")
    print("RESULT " + json.dumps(result, separators=(",", ":")), flush=True)


if __name__ == "__main__":
    main()

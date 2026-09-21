# Benchmark eval image: load generator + lm-eval-harness against an
# OpenAI-compatible endpoint. Built and published by
# .github/workflows/bench-image.yml; override with IMAGE_BENCH.
FROM python:3.12-slim

ENV PIP_ROOT_USER_ACTION=ignore \
    PIP_NO_CACHE_DIR=1 \
    PYTHONUNBUFFERED=1

# lm-eval depends on torch; the CPU wheel keeps the image a fraction of the
# size of the default CUDA build. Nothing here runs on the GPU.
RUN pip install --quiet torch --index-url https://download.pytorch.org/whl/cpu \
 && pip install --quiet "lm_eval[api]>=0.4.8"

WORKDIR /app
COPY bench.py /app/bench.py

ENTRYPOINT ["python3", "/app/bench.py"]

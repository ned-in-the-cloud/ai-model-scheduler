# REFERENCE ONLY — the app self-registers this job (as "ams-indexer") on
# every catalog refresh; you never need to run this file by hand. It exists
# so you can read what runs on your box.
job "ams-indexer" {
  type = "batch"

  parameterized {}

  meta {
    managed-by = "ai-model-scheduler"
    kind       = "helper"
  }

  group "helper" {
    restart {
      attempts = 0
      mode     = "fail"
    }

    task "server" {
      driver = "podman"

      config {
        image   = "docker.io/library/alpine:3.20"
        command = "/bin/sh"
        # Scans /models for *.gguf files and Hugging Face directories
        # (identified by config.json) and prints one JSON object per model
        # to stdout. The app reads stdout via the allocation logs API.
        args    = ["-c", "<see internal/jobspec/helpers.go>"]
        volumes = ["/mnt/models:/models:ro"]
      }

      resources {
        cpu    = 200
        memory = 128
      }
    }
  }
}

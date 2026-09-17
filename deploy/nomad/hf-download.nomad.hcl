# REFERENCE ONLY — the app self-registers this job (as "ams-hf-download")
# before every dispatch; you never need to run this file by hand.
job "ams-hf-download" {
  type = "batch"

  parameterized {
    meta_required = ["repo_id", "dest"]
    meta_optional = ["revision", "include"]
  }

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
        image   = "docker.io/library/python:3.12-slim"
        command = "/bin/sh"
        # pip-installs huggingface_hub and runs:
        #   hf download $NOMAD_META_repo_id --local-dir /models/$NOMAD_META_dest
        # honoring optional revision/include dispatch meta.
        args    = ["-c", "<see internal/jobspec/helpers.go>"]
        volumes = ["/mnt/models:/models"]
      }

      # HF_TOKEN is injected from the app's config when set.

      # Memory covers page cache for buffered writes to the share, not just
      # the Python process — cgroup v2 charges it to the task.
      resources {
        cpu    = 500
        memory = 4096
      }
    }
  }
}

# Extra config for the local `nomad agent -dev` used in integration testing:
# the docker driver must allow host volume mounts, like the podman driver on
# the real box.
plugin "docker" {
  config {
    volumes {
      enabled = true
    }
  }
}

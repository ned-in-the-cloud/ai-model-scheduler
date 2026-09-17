# Remote box setup

One-time preparation of the Linux inference box so the scheduler can drive it.
Everything the UI does goes through the Nomad HTTP API (port 4646) — once this
guide is complete, no further access to the box is needed.

Assumptions: a systemd-based distro (Ubuntu/Debian shown), an NVIDIA GPU, and
a NAS share for models.

## 1. Mount the NAS share

The scheduler defaults to `/mnt/models` (configurable via `MODEL_ROOT_HOST`).

```bash
sudo mkdir -p /mnt/models
# NFS example — add to /etc/fstab:
#   nas.local:/volume1/models  /mnt/models  nfs  defaults,_netdev  0 0
# SMB example:
#   //nas.local/models  /mnt/models  cifs  credentials=/etc/nas-creds,uid=root,_netdev  0 0
sudo mount -a
ls /mnt/models   # sanity check
```

## 2. Install podman

```bash
sudo apt install -y podman
sudo systemctl enable --now podman.socket   # driver talks to the root podman socket
```

## 3. NVIDIA driver + container toolkit + CDI

```bash
# Driver (skip if nvidia-smi already works)
sudo ubuntu-drivers install   # or your distro's equivalent
nvidia-smi

# NVIDIA container toolkit (for CDI spec generation)
# See https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html
sudo apt install -y nvidia-container-toolkit

# Generate the CDI spec podman uses for --device nvidia.com/gpu=all
sudo nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml
# Verify:
sudo podman run --rm --device nvidia.com/gpu=all docker.io/library/ubuntu:24.04 nvidia-smi
```

Regenerate the CDI spec after every NVIDIA driver upgrade.

## 4. Install Nomad + the podman driver

```bash
wget -O - https://apt.releases.hashicorp.com/gpg | sudo gpg --dearmor -o /usr/share/keyrings/hashicorp-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/hashicorp-archive-keyring.gpg] https://apt.releases.hashicorp.com $(lsb_release -cs) main" | sudo tee /etc/apt/sources.list.d/hashicorp.list
sudo apt update && sudo apt install -y nomad nomad-driver-podman

sudo mkdir -p /opt/nomad/plugins
sudo ln -sf /usr/bin/nomad-driver-podman /opt/nomad/plugins/
```

## 5. Nomad agent config

`/etc/nomad.d/nomad.hcl` — single node acting as both server and client:

```hcl
data_dir   = "/opt/nomad/data"
plugin_dir = "/opt/nomad/plugins"
bind_addr  = "0.0.0.0"

server {
  enabled          = true
  bootstrap_expect = 1
}

client {
  enabled = true
}

plugin "nomad-driver-podman" {
  config {
    volumes {
      enabled = true   # REQUIRED: lets jobs mount /mnt/models
    }
  }
}
```

```bash
sudo systemctl enable --now nomad
nomad node status        # node should be ready
nomad node status -self | grep -i driver   # podman driver healthy
```

## 6. Firewall

Allow the scheduler machine (Windows box / Pi) to reach the Nomad API and the
model endpoints:

```bash
sudo ufw allow from 192.168.1.0/24 to any port 4646 proto tcp   # Nomad API
sudo ufw allow from 192.168.1.0/24 to any port 8000:8999 proto tcp  # model endpoints
```

The Nomad API is unauthenticated unless you enable ACLs. On a trusted home
LAN that may be fine; otherwise enable ACLs and give the scheduler a token
via `NOMAD_TOKEN`.

## 7. Smoke test from the scheduler machine

```powershell
curl http://<box-ip>:4646/v1/status/leader   # should return an address
```

Then start the UI with `NOMAD_ADDR=http://<box-ip>:4646` and the health badge
in the top bar should turn green.

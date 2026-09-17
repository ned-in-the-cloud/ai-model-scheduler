IMAGE ?= ai-model-scheduler
TAG   ?= dev

.PHONY: build test vet run image image-multiarch dev-nomad

build:
	go build -o bin/scheduler ./cmd/scheduler

test:
	go test ./...

vet:
	go vet ./...

run: build
	./bin/scheduler

image:
	docker build -t $(IMAGE):$(TAG) .

image-multiarch:
	docker buildx build --platform linux/amd64,linux/arm64 -t $(IMAGE):$(TAG) .

# Local Nomad dev agent with the docker driver for integration testing.
# --cgroupns=host + the cgroup mount are required under Docker Desktop.
dev-nomad:
	docker run -d --name nomad-dev --privileged --cgroupns=host \
		-v /sys/fs/cgroup:/sys/fs/cgroup:rw \
		-p 4646:4646 -e NOMAD_SKIP_DOCKER_IMAGE_WARN=1 \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v $(CURDIR)/deploy/dev/docker-volumes.hcl:/etc/nomad-extra/docker.hcl:ro \
		hashicorp/nomad agent -dev -bind=0.0.0.0 -config=/etc/nomad-extra/docker.hcl

# Fake model files in the Docker VM's /mnt/models for catalog testing.
dev-models:
	docker run --rm -v /mnt/models:/m alpine sh -c \
		"dd if=/dev/zero of=/m/test.gguf bs=1024 count=100 2>/dev/null; \
		 mkdir -p /m/qwen-test && echo '{}' > /m/qwen-test/config.json"

dev-clean:
	docker rm -f nomad-dev

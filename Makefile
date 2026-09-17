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
dev-nomad:
	docker run --rm --name nomad-dev --privileged -p 4646:4646 \
		-v /var/run/docker.sock:/var/run/docker.sock \
		hashicorp/nomad agent -dev -bind=0.0.0.0

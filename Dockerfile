# Multi-arch build: docker buildx build --platform linux/amd64,linux/arm64 .
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/scheduler ./cmd/scheduler \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/scheduler /scheduler
# Benchmark suites, datasets, and results. Mount a volume here to keep them
# across container recreates; the empty dir is copied in with nonroot
# ownership so a fresh named volume is writable.
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV DATA_DIR=/data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/scheduler"]

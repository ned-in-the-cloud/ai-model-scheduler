// Package config loads application configuration from environment variables,
// optionally merged over a YAML file referenced by CONFIG_FILE. Environment
// variables always win.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Images holds the container images used for inference runtimes and helper
// jobs. Runtimes have one image per GPU vendor; an empty value means the
// combination is unsupported until the user configures an image for it.
type Images struct {
	LlamaCPP      string `yaml:"llamacpp"`
	LlamaCPPCUDA  string `yaml:"llamacpp_cuda"`
	LlamaCPPROCm  string `yaml:"llamacpp_rocm"`
	LlamaCPPIntel string `yaml:"llamacpp_intel"`
	VLLM          string `yaml:"vllm"`
	VLLMROCm      string `yaml:"vllm_rocm"`
	VLLMIntel     string `yaml:"vllm_intel"`
	Ollama        string `yaml:"ollama"`
	OllamaROCm    string `yaml:"ollama_rocm"`
	OllamaIntel   string `yaml:"ollama_intel"`
	Indexer       string `yaml:"indexer"`
	HFDownload    string `yaml:"hf_download"`
	GPUStats      string `yaml:"gpu_stats"`
}

// Config is the full application configuration.
type Config struct {
	NomadAddr  string `yaml:"nomad_addr"`
	NomadToken string `yaml:"nomad_token"`

	// ModelRootHost is where the NAS share is mounted on the remote box.
	ModelRootHost string `yaml:"model_root_host"`
	// ModelMount is the mount point inside inference containers.
	ModelMount string `yaml:"model_mount"`

	// Driver is the Nomad task driver: "podman" on the real box, "docker" for
	// local dev against `nomad agent -dev`.
	Driver string `yaml:"driver"`

	// ImagePullTimeout is how long a task may spend pulling its container
	// image before the allocation fails. The drivers default to 5m, which
	// is far too short for multi-GB ROCm/CUDA images.
	ImagePullTimeout string `yaml:"image_pull_timeout"`

	HFToken string `yaml:"hf_token"`

	ListenAddr    string `yaml:"listen_addr"`
	BasicAuthUser string `yaml:"basic_auth_user"`
	BasicAuthPass string `yaml:"basic_auth_pass"`

	PortMin int `yaml:"port_min"`
	PortMax int `yaml:"port_max"`

	CatalogTTL time.Duration `yaml:"catalog_ttl"`

	Images Images `yaml:"images"`
}

func defaults() Config {
	return Config{
		NomadAddr:     "http://127.0.0.1:4646",
		ModelRootHost: "/mnt/models",
		ModelMount:    "/models",
		Driver:           "podman",
		ImagePullTimeout: "45m",
		ListenAddr:       ":8080",
		PortMin:       8000,
		PortMax:       8999,
		CatalogTTL:    5 * time.Minute,
		Images: Images{
			LlamaCPP:      "ghcr.io/ggml-org/llama.cpp:server",
			LlamaCPPCUDA:  "ghcr.io/ggml-org/llama.cpp:server-cuda",
			LlamaCPPROCm:  "ghcr.io/ggml-org/llama.cpp:server-rocm",
			LlamaCPPIntel: "ghcr.io/ggml-org/llama.cpp:server-intel",
			VLLM:          "docker.io/vllm/vllm-openai:latest",
			VLLMROCm:      "docker.io/rocm/vllm:latest",
			VLLMIntel:     "", // no well-known default; set IMAGE_VLLM_INTEL
			Ollama:        "docker.io/ollama/ollama:latest",
			OllamaROCm:    "docker.io/ollama/ollama:rocm",
			OllamaIntel:   "", // no well-known default; set IMAGE_OLLAMA_INTEL
			Indexer:       "docker.io/library/alpine:3.20",
			HFDownload:    "docker.io/library/python:3.12-slim",
			// glibc-based: the CDI-injected nvidia-smi binary won't run on musl.
			GPUStats: "docker.io/library/debian:bookworm-slim",
		},
	}
}

// Load builds the configuration: defaults, then the optional YAML file, then
// environment variables on top.
func Load() (Config, error) {
	cfg := defaults()

	if path := os.Getenv("CONFIG_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("reading CONFIG_FILE: %w", err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parsing CONFIG_FILE: %w", err)
		}
	}

	strVar(&cfg.NomadAddr, "NOMAD_ADDR")
	strVar(&cfg.NomadToken, "NOMAD_TOKEN")
	strVar(&cfg.ModelRootHost, "MODEL_ROOT_HOST")
	strVar(&cfg.ModelMount, "MODEL_MOUNT")
	strVar(&cfg.Driver, "NOMAD_DRIVER")
	strVar(&cfg.ImagePullTimeout, "IMAGE_PULL_TIMEOUT")
	strVar(&cfg.HFToken, "HF_TOKEN")
	strVar(&cfg.ListenAddr, "LISTEN_ADDR")
	strVar(&cfg.BasicAuthUser, "BASIC_AUTH_USER")
	strVar(&cfg.BasicAuthPass, "BASIC_AUTH_PASS")
	intVar(&cfg.PortMin, "PORT_MIN")
	intVar(&cfg.PortMax, "PORT_MAX")
	durVar(&cfg.CatalogTTL, "CATALOG_TTL")
	strVar(&cfg.Images.LlamaCPP, "IMAGE_LLAMACPP")
	strVar(&cfg.Images.LlamaCPPCUDA, "IMAGE_LLAMACPP_CUDA")
	strVar(&cfg.Images.LlamaCPPROCm, "IMAGE_LLAMACPP_ROCM")
	strVar(&cfg.Images.LlamaCPPIntel, "IMAGE_LLAMACPP_INTEL")
	strVar(&cfg.Images.VLLM, "IMAGE_VLLM")
	strVar(&cfg.Images.VLLMROCm, "IMAGE_VLLM_ROCM")
	strVar(&cfg.Images.VLLMIntel, "IMAGE_VLLM_INTEL")
	strVar(&cfg.Images.Ollama, "IMAGE_OLLAMA")
	strVar(&cfg.Images.OllamaROCm, "IMAGE_OLLAMA_ROCM")
	strVar(&cfg.Images.OllamaIntel, "IMAGE_OLLAMA_INTEL")
	strVar(&cfg.Images.Indexer, "IMAGE_INDEXER")
	strVar(&cfg.Images.HFDownload, "IMAGE_HFDOWNLOAD")
	strVar(&cfg.Images.GPUStats, "IMAGE_GPUSTATS")

	return cfg, cfg.validate()
}

func (c Config) validate() error {
	if c.NomadAddr == "" {
		return fmt.Errorf("NOMAD_ADDR must be set")
	}
	if c.Driver != "podman" && c.Driver != "docker" {
		return fmt.Errorf("NOMAD_DRIVER must be podman or docker, got %q", c.Driver)
	}
	if c.PortMin <= 0 || c.PortMax < c.PortMin {
		return fmt.Errorf("invalid port range %d-%d", c.PortMin, c.PortMax)
	}
	if c.ImagePullTimeout != "" {
		if _, err := time.ParseDuration(c.ImagePullTimeout); err != nil {
			return fmt.Errorf("invalid IMAGE_PULL_TIMEOUT %q: %w", c.ImagePullTimeout, err)
		}
	}
	if (c.BasicAuthUser == "") != (c.BasicAuthPass == "") {
		return fmt.Errorf("BASIC_AUTH_USER and BASIC_AUTH_PASS must be set together")
	}
	return nil
}

// BasicAuthEnabled reports whether the UI should require basic auth.
func (c Config) BasicAuthEnabled() bool { return c.BasicAuthUser != "" }

func strVar(dst *string, key string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

func intVar(dst *int, key string) {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
}

func durVar(dst *time.Duration, key string) {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			*dst = d
		}
	}
}

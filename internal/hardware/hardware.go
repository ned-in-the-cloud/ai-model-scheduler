// Package hardware discovers GPUs on the remote box by dispatching the
// ams-gpu-probe batch job and parsing the NDJSON it prints to stdout,
// following the same pattern as the model catalog. Results are cached; GPUs
// change rarely, so the TTL is long and the UI offers a manual rescan.
package hardware

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"ai-model-scheduler/internal/config"
	"ai-model-scheduler/internal/jobspec"
	"ai-model-scheduler/internal/nomadapi"
)

// GPU is one detected display device.
type GPU struct {
	Index  int    `json:"index"`
	Vendor string `json:"vendor"` // nvidia | amd | intel | other
	Name   string `json:"name"`
	PCI    string `json:"pci"`
}

// Deployable reports whether the app knows how to schedule work on this GPU.
func (g GPU) Deployable() bool {
	switch g.Vendor {
	case "nvidia", "amd", "intel":
		return true
	}
	return false
}

const probeTTL = time.Hour

// Service caches probe results.
type Service struct {
	nomad *nomadapi.Client
	env   jobspec.Env
	log   *slog.Logger

	mu      sync.Mutex
	gpus    []GPU
	fetched time.Time
}

func New(nomad *nomadapi.Client, cfg config.Config, log *slog.Logger) *Service {
	return &Service{nomad: nomad, env: jobspec.EnvFromConfig(cfg), log: log}
}

// Entries returns the cached GPU list, probing first if the cache is empty
// or older than the TTL. On probe failure with a warm cache, stale data is
// served.
func (s *Service) Entries(ctx context.Context) ([]GPU, time.Time, error) {
	s.mu.Lock()
	fresh := !s.fetched.IsZero() && time.Since(s.fetched) < probeTTL
	gpus, fetched := s.gpus, s.fetched
	s.mu.Unlock()
	if fresh {
		return gpus, fetched, nil
	}
	if err := s.Refresh(ctx); err != nil {
		if fetched.IsZero() {
			return nil, time.Time{}, err
		}
		s.log.Warn("gpu probe failed, serving stale data", "err", err)
		return gpus, fetched, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gpus, s.fetched, nil
}

// Refresh dispatches the probe job, waits for it, and replaces the cache.
func (s *Service) Refresh(ctx context.Context) error {
	if err := s.nomad.Register(jobspec.GPUProbe(s.env)); err != nil {
		return fmt.Errorf("registering gpu probe job: %w", err)
	}
	dispatchID, err := s.nomad.Dispatch(jobspec.GPUProbeJobID, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err := s.nomad.Stop(dispatchID, true); err != nil {
			s.log.Warn("purging gpu probe dispatch", "id", dispatchID, "err", err)
		}
	}()

	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res, err := s.nomad.WaitForBatch(waitCtx, dispatchID)
	if err != nil {
		return err
	}
	if res.ClientStatus != "complete" {
		return fmt.Errorf("gpu probe failed: %s", s.nomad.BatchFailureReason(res.AllocID))
	}
	stdout, logErr := s.nomad.ReadAllLogs(res.AllocID, nomadapi.TaskName, "stdout")
	if logErr != nil && stdout == "" {
		return fmt.Errorf("reading gpu probe output: %w", logErr)
	}

	gpus := parseProbe(stdout)
	s.mu.Lock()
	s.gpus, s.fetched = gpus, time.Now()
	s.mu.Unlock()
	s.log.Info("gpu probe complete", "gpus", len(gpus))
	return nil
}

// parseProbe decodes NDJSON probe output, tolerating stray noise and log
// prefixes around each JSON object.
func parseProbe(out string) []GPU {
	gpus := []GPU{}
	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, "{")
		if i < 0 {
			continue
		}
		var g GPU
		if err := json.Unmarshal([]byte(line[i:]), &g); err != nil {
			continue
		}
		if g.Vendor != "" && g.Name != "" {
			gpus = append(gpus, g)
		}
	}
	return gpus
}

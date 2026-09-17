package hardware

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ai-model-scheduler/internal/jobspec"
	"ai-model-scheduler/internal/nomadapi"
)

// GPUStats is one GPU's live utilization sample. Sentinels: -1 means the
// value is unavailable for percentages/temperature/power; 0 for VRAM totals.
type GPUStats struct {
	Vendor      string  `json:"vendor"`
	Name        string  `json:"name"`
	PCI         string  `json:"pci,omitempty"`
	UtilPercent float64 `json:"util_percent"`
	VRAMUsed    int64   `json:"vram_used_bytes"`
	VRAMTotal   int64   `json:"vram_total_bytes"`
	VRAMPercent float64 `json:"vram_percent"`
	TempC       float64 `json:"temp_c"`
	PowerW      float64 `json:"power_w"`
}

type gpuSample struct {
	TS   int64      `json:"ts"`
	GPUs []GPUStats `json:"gpus"`
}

// ensureAgentEvery bounds how often the (idempotent) agent registration is
// re-submitted, so dashboard polling doesn't spam Nomad with evaluations.
const ensureAgentEvery = 10 * time.Minute

// Stats returns the latest GPU utilization sample from the ams-gpu-stats
// agent, starting the agent if needed. Returns (nil, nil) when the box has
// no deployable GPUs.
func (s *Service) Stats(ctx context.Context) ([]GPUStats, error) {
	probed, _, err := s.Entries(ctx)
	if err != nil {
		return nil, err
	}
	deployable, hasNvidia := false, false
	for _, g := range probed {
		if g.Deployable() {
			deployable = true
			if g.Vendor == "nvidia" {
				hasNvidia = true
			}
		}
	}
	if !deployable {
		return nil, nil
	}

	if err := s.ensureAgent(hasNvidia); err != nil {
		return nil, fmt.Errorf("starting gpu stats agent: %w", err)
	}

	alloc, err := s.nomad.LatestAlloc(jobspec.GPUStatsJobID)
	if err != nil {
		return nil, err
	}
	if alloc == nil || alloc.ClientStatus != "running" {
		return nil, fmt.Errorf("gpu stats agent starting")
	}
	tail, err := s.nomad.TailLogs(alloc.ID, nomadapi.TaskName, "stdout", 8*1024)
	if err != nil && tail == "" {
		return nil, fmt.Errorf("reading gpu stats: %w", err)
	}
	sample, ok := latestSample(tail)
	if !ok {
		return nil, fmt.Errorf("gpu stats agent starting")
	}
	return s.enrichNames(sample.GPUs), nil
}

func (s *Service) ensureAgent(nvidia bool) error {
	s.mu.Lock()
	fresh := s.agentEnsured != nil && *s.agentEnsured == nvidia &&
		time.Since(s.agentEnsuredAt) < ensureAgentEvery
	s.mu.Unlock()
	if fresh {
		return nil
	}
	if err := s.nomad.Register(jobspec.GPUStatsAgent(s.env, nvidia)); err != nil {
		return err
	}
	s.mu.Lock()
	s.agentEnsured, s.agentEnsuredAt = &nvidia, time.Now()
	s.mu.Unlock()
	return nil
}

// latestSample parses the newest complete sample line from a log tail.
func latestSample(tail string) (gpuSample, bool) {
	lines := strings.Split(tail, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		idx := strings.Index(lines[i], "{")
		if idx < 0 || !strings.Contains(lines[i], `"gpus"`) {
			continue
		}
		var sample gpuSample
		if err := json.Unmarshal([]byte(lines[i][idx:]), &sample); err != nil {
			continue
		}
		return sample, true
	}
	return gpuSample{}, false
}

// enrichNames fills in display names and VRAM percentages. AMD entries carry
// only a PCI address; when the probe found exactly one GPU of that vendor,
// its marketing name is used.
func (s *Service) enrichNames(gpus []GPUStats) []GPUStats {
	s.mu.Lock()
	probed := s.gpus
	s.mu.Unlock()

	byVendor := map[string][]GPU{}
	for _, p := range probed {
		byVendor[p.Vendor] = append(byVendor[p.Vendor], p)
	}
	for i := range gpus {
		g := &gpus[i]
		if g.Name == "" {
			if candidates := byVendor[g.Vendor]; len(candidates) == 1 {
				g.Name = candidates[0].Name
			} else {
				g.Name = strings.TrimSpace(g.Vendor + " " + g.PCI)
			}
		}
		if g.VRAMTotal > 0 {
			g.VRAMPercent = float64(g.VRAMUsed) / float64(g.VRAMTotal) * 100
		}
	}
	return gpus
}

// Package deploy orchestrates model deployments: validation, port selection,
// job registration, and teardown.
package deploy

import (
	"fmt"

	"ai-model-scheduler/internal/config"
	"ai-model-scheduler/internal/jobspec"
	"ai-model-scheduler/internal/nomadapi"
)

// Service coordinates deployments against a Nomad cluster.
type Service struct {
	nomad *nomadapi.Client
	cfg   config.Config
	env   jobspec.Env
}

func New(nomad *nomadapi.Client, cfg config.Config) *Service {
	return &Service{nomad: nomad, cfg: cfg, env: jobspec.EnvFromConfig(cfg)}
}

// Deploy validates the request, resolves the port, and registers the job.
// It returns immediately after registration; callers observe progress through
// the deployments list, which reflects allocation state.
func (s *Service) Deploy(p jobspec.Params) error {
	if err := p.Validate(); err != nil {
		return err
	}

	deps, err := s.nomad.ListManaged()
	if err != nil {
		return fmt.Errorf("checking existing deployments: %w", err)
	}
	for _, d := range deps {
		if d.Name == p.Name && d.Status != "dead" {
			return fmt.Errorf("deployment %q already exists", p.Name)
		}
	}

	if p.Port == 0 {
		port, err := s.suggestPort(deps)
		if err != nil {
			return err
		}
		p.Port = port
	} else {
		if p.Port < s.cfg.PortMin || p.Port > s.cfg.PortMax {
			return fmt.Errorf("port %d outside allowed range %d-%d", p.Port, s.cfg.PortMin, s.cfg.PortMax)
		}
		for _, d := range deps {
			if d.Port == p.Port && d.Status != "dead" {
				return fmt.Errorf("port %d already used by deployment %q", p.Port, d.Name)
			}
		}
	}

	job, err := jobspec.Build(p, s.env)
	if err != nil {
		return err
	}
	return s.nomad.Register(job)
}

// Stop stops (and optionally purges) a deployment by name.
func (s *Service) Stop(name string, purge bool) error {
	return s.nomad.Stop(nomadapi.JobPrefix+name, purge)
}

// SuggestPort returns a free port from the configured range, considering
// ports of all non-dead managed deployments.
func (s *Service) SuggestPort() (int, error) {
	deps, err := s.nomad.ListManaged()
	if err != nil {
		return 0, err
	}
	return s.suggestPort(deps)
}

func (s *Service) suggestPort(deps []nomadapi.Deployment) (int, error) {
	used := make(map[int]bool, len(deps))
	for _, d := range deps {
		if d.Status != "dead" && d.Port != 0 {
			used[d.Port] = true
		}
	}
	for p := s.cfg.PortMin; p <= s.cfg.PortMax; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free ports in range %d-%d", s.cfg.PortMin, s.cfg.PortMax)
}

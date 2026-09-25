package bench

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hashicorp/nomad/api"

	"ai-model-scheduler/internal/deploy"
	"ai-model-scheduler/internal/hardware"
	"ai-model-scheduler/internal/jobspec"
	"ai-model-scheduler/internal/nomadapi"
)

// NomadCluster adapts the real services to the Cluster interface.
type NomadCluster struct {
	Nomad    *nomadapi.Client
	Deployer *deploy.Service
	Hardware *hardware.Service
}

func (c *NomadCluster) ListManaged() ([]nomadapi.Deployment, error) { return c.Nomad.ListManaged() }

func (c *NomadCluster) DeploymentParams(name string) (jobspec.Params, bool) {
	meta, err := c.Nomad.JobMeta(nomadapi.JobPrefix + name)
	if err != nil {
		return jobspec.Params{}, false
	}
	raw := meta[nomadapi.ParamsKey]
	if raw == "" {
		return jobspec.Params{}, false
	}
	var p jobspec.Params
	if json.Unmarshal([]byte(raw), &p) != nil || p.Name == "" {
		return jobspec.Params{}, false
	}
	return p, true
}

func (c *NomadCluster) Deploy(p jobspec.Params) error      { return c.Deployer.Deploy(p) }
func (c *NomadCluster) Stop(name string, purge bool) error { return c.Deployer.Stop(name, purge) }
func (c *NomadCluster) RegisterJob(job *api.Job) error     { return c.Nomad.Register(job) }
func (c *NomadCluster) PurgeJob(jobID string) error        { return c.Nomad.Stop(jobID, true) }
func (c *NomadCluster) FailureReason(allocID string) string {
	return c.Nomad.BatchFailureReason(allocID)
}

func (c *NomadCluster) TaskExits(allocID string) []string {
	events, err := c.Nomad.AllocEvents(allocID)
	if err != nil {
		return nil
	}
	var exits []string
	for _, ev := range events {
		// A normal stop also ends in "Terminated", but only after a
		// "Killing" event; the runner reads exits before tearing down.
		if ev.Type == "Terminated" {
			exits = append(exits, ev.Message)
		}
	}
	return exits
}

func (c *NomadCluster) BatchStatus(jobID string) (string, string, error) {
	alloc, err := c.Nomad.LatestAlloc(jobID)
	if err != nil || alloc == nil {
		return "", "", err
	}
	return alloc.ID, alloc.ClientStatus, nil
}

func (c *NomadCluster) ReadLogs(allocID, stream string, all bool) (string, error) {
	if all {
		return c.Nomad.ReadAllLogs(allocID, nomadapi.TaskName, stream)
	}
	return c.Nomad.TailLogs(allocID, nomadapi.TaskName, stream, 4*1024)
}

func (c *NomadCluster) PurgeJobsWithPrefix(prefix string) error {
	stubs, err := c.Nomad.ListByPrefix(prefix)
	if err != nil {
		return err
	}
	for _, s := range stubs {
		if strings.HasPrefix(s.ID, prefix) {
			_ = c.Nomad.Stop(s.ID, true)
		}
	}
	return nil
}

func (c *NomadCluster) GPUStats(ctx context.Context) ([]hardware.GPUStats, error) {
	return c.Hardware.Stats(ctx)
}

package nomadapi

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/nomad/api"
)

// Meta keys/values used to tag jobs owned by this application.
const (
	ManagedByKey   = "managed-by"
	ManagedByValue = "ai-model-scheduler"
	KindKey        = "kind"
	KindInference  = "inference"
	KindHelper     = "helper"
	RuntimeKey     = "runtime"
	ModelKey       = "model"
	GPUKey         = "gpu"
	// ParamsKey stores the full deployment parameters as JSON so a stopped
	// job can be relaunched with every setting intact.
	ParamsKey = "params"

	// JobPrefix prefixes every inference job ID (e.g. model-llama3).
	JobPrefix = "model-"
	// PortLabel is the network port label used in inference jobs.
	PortLabel = "api"
)

// Deployment is a managed inference job with its current allocation state.
type Deployment struct {
	JobID       string `json:"job_id"`
	Name        string `json:"name"`
	Runtime     string `json:"runtime"`
	Model       string `json:"model"`
	Status      string `json:"status"`       // job status: running | pending | dead
	AllocID     string `json:"alloc_id"`     // latest allocation, if any
	AllocStatus string `json:"alloc_status"` // client status of that allocation
	Healthy     bool   `json:"healthy"`
	Endpoint    string `json:"endpoint,omitempty"` // host:port once allocated
	Port        int    `json:"port,omitempty"`
	GPU         string `json:"gpu,omitempty"` // vendor: nvidia | amd | intel; empty = CPU
	CPUMHz      int    `json:"cpu_mhz,omitempty"`
	MemMB       int    `json:"mem_mb,omitempty"`

	// Live usage, filled only while the allocation is running.
	UsageCPUMHz   float64 `json:"usage_cpu_mhz,omitempty"`
	UsageMemBytes int64   `json:"usage_mem_bytes,omitempty"`
}

// ListManaged returns all inference jobs owned by this application, newest
// first, with allocation state and endpoint resolved.
func (c *Client) ListManaged() ([]Deployment, error) {
	stubs, _, err := c.c.Jobs().List(nil)
	if err != nil {
		return nil, fmt.Errorf("listing jobs: %w", err)
	}

	out := []Deployment{}
	for _, stub := range stubs {
		if !strings.HasPrefix(stub.ID, JobPrefix) {
			continue
		}
		job, _, err := c.c.Jobs().Info(stub.ID, nil)
		if err != nil {
			continue // job may have been purged between list and info
		}
		if job.Meta[ManagedByKey] != ManagedByValue || job.Meta[KindKey] != KindInference {
			continue
		}
		gpu := job.Meta[GPUKey]
		switch gpu {
		case "true": // legacy boolean meta from before vendor selection; was CUDA-only
			gpu = "nvidia"
		case "false":
			gpu = ""
		}
		d := Deployment{
			JobID:   stub.ID,
			Name:    strings.TrimPrefix(stub.ID, JobPrefix),
			Runtime: job.Meta[RuntimeKey],
			Model:   job.Meta[ModelKey],
			Status:  stub.Status,
			GPU:     gpu,
		}
		if res := taskResources(job); res != nil {
			if res.CPU != nil {
				d.CPUMHz = *res.CPU
			}
			if res.MemoryMB != nil {
				d.MemMB = *res.MemoryMB
			}
		}
		if alloc, err := c.LatestAlloc(stub.ID); err == nil && alloc != nil {
			d.AllocID = alloc.ID
			d.AllocStatus = alloc.ClientStatus
			d.Healthy = alloc.ClientStatus == api.AllocClientStatusRunning
			d.Endpoint, d.Port = allocEndpoint(alloc)
			if d.Healthy {
				if usage, err := c.AllocStats(alloc.ID); err == nil {
					d.UsageCPUMHz = usage.CPUMHz
					d.UsageMemBytes = usage.MemBytes
				}
			}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// JobStub is a minimal view of a registered job.
type JobStub struct {
	ID         string
	Status     string
	SubmitTime int64 // unix nanos
}

// ListByPrefix returns jobs whose ID starts with prefix.
func (c *Client) ListByPrefix(prefix string) ([]JobStub, error) {
	stubs, _, err := c.c.Jobs().List(&api.QueryOptions{Prefix: prefix})
	if err != nil {
		return nil, fmt.Errorf("listing jobs with prefix %s: %w", prefix, err)
	}
	out := make([]JobStub, 0, len(stubs))
	for _, s := range stubs {
		out = append(out, JobStub{ID: s.ID, Status: s.Status, SubmitTime: s.SubmitTime})
	}
	return out, nil
}

// JobMeta returns a job's merged meta map.
func (c *Client) JobMeta(jobID string) (map[string]string, error) {
	job, _, err := c.c.Jobs().Info(jobID, nil)
	if err != nil {
		return nil, fmt.Errorf("fetching job %s: %w", jobID, err)
	}
	return job.Meta, nil
}

// Register submits (creates or updates) a job.
func (c *Client) Register(job *api.Job) error {
	_, _, err := c.c.Jobs().Register(job, nil)
	if err != nil {
		return fmt.Errorf("registering job: %w", err)
	}
	return nil
}

// Stop stops a job, optionally purging it from Nomad's state entirely.
func (c *Client) Stop(jobID string, purge bool) error {
	_, _, err := c.c.Jobs().Deregister(jobID, purge, nil)
	if err != nil {
		return fmt.Errorf("stopping job %s: %w", jobID, err)
	}
	return nil
}

// JobExists reports whether a job with the given ID is registered (in any
// status, including dead).
func (c *Client) JobExists(jobID string) (bool, error) {
	_, _, err := c.c.Jobs().Info(jobID, nil)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// taskResources returns the resources of the first task in the first group.
func taskResources(job *api.Job) *api.Resources {
	if len(job.TaskGroups) == 0 || len(job.TaskGroups[0].Tasks) == 0 {
		return nil
	}
	return job.TaskGroups[0].Tasks[0].Resources
}

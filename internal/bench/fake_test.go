package bench

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/hashicorp/nomad/api"

	"ai-model-scheduler/internal/config"
	"ai-model-scheduler/internal/hardware"
	"ai-model-scheduler/internal/jobspec"
	"ai-model-scheduler/internal/nomadapi"
)

type jobspec_Params = jobspec.Params

func testEnv() jobspec.Env {
	return jobspec.Env{
		Driver: "podman", ModelRootHost: "/mnt/models", ModelMount: "/models",
		Images: config.Images{VLLM: "img/vllm", Bench: "img/bench"},
	}
}

// fakeCluster simulates the parts of Nomad the runner touches.
type fakeCluster struct {
	mu          sync.Mutex
	deployments map[string]*nomadapi.Deployment
	params      map[string]jobspec.Params
	deployed    map[string]int  // deploy calls per name
	stopped     map[string]bool // stop calls (any purge value)
	purged      map[string]bool
	failDeploy  map[string]bool // "bench-*-<idx>" patterns whose allocation fails
	jobs        map[string]int  // registered eval jobs -> poll count
	jobsSeen    int
	registered  []*api.Job // every job passed to RegisterJob, in order
	hang        bool       // eval jobs never complete
	nextPort    int
}

func newFakeCluster() *fakeCluster {
	return &fakeCluster{
		deployments: map[string]*nomadapi.Deployment{},
		params:      map[string]jobspec.Params{},
		deployed:    map[string]int{},
		stopped:     map[string]bool{},
		purged:      map[string]bool{},
		failDeploy:  map[string]bool{},
		jobs:        map[string]int{},
		nextPort:    8100,
	}
}

func (f *fakeCluster) existing(name string, port int) {
	f.deployments[name] = &nomadapi.Deployment{Name: name, Runtime: "vllm", Model: "m", GPU: "nvidia", Status: "running", Healthy: true, Port: port, Endpoint: fmt.Sprintf("10.0.0.1:%d", port), AllocID: "alloc-" + name}
	f.params[name] = jobspec.Params{Name: name, Runtime: "vllm", Model: "m", GPU: "nvidia", Port: port}
}

func (f *fakeCluster) deployedBench(name string) {
	f.deployments[name] = &nomadapi.Deployment{Name: name, Status: "running", Healthy: true}
}

func (f *fakeCluster) ListManaged() ([]nomadapi.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []nomadapi.Deployment
	for _, d := range f.deployments {
		out = append(out, *d)
	}
	return out, nil
}

func (f *fakeCluster) DeploymentParams(name string) (jobspec.Params, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.params[name]
	return p, ok
}

func (f *fakeCluster) shouldFail(name string) bool {
	for pat := range f.failDeploy {
		// pattern "bench-*-N": match prefix and suffix around the wildcard
		pre, post, _ := strings.Cut(pat, "*")
		if strings.HasPrefix(name, pre) && strings.HasSuffix(name, post) {
			return true
		}
	}
	return false
}

func (f *fakeCluster) Deploy(p jobspec.Params) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.deployments[p.Name]; ok && d.Status != "dead" {
		return fmt.Errorf("deployment %q already exists", p.Name)
	}
	if p.Port == 0 {
		p.Port = f.nextPort
		f.nextPort++
	}
	f.deployed[p.Name]++
	f.params[p.Name] = p
	d := &nomadapi.Deployment{Name: p.Name, Runtime: p.Runtime, Model: p.Model, GPU: p.GPU, Port: p.Port, AllocID: "alloc-" + p.Name}
	if f.shouldFail(p.Name) {
		d.Status, d.AllocStatus = "pending", "failed"
	} else {
		d.Status, d.AllocStatus, d.Healthy = "running", "running", true
		d.Endpoint = fmt.Sprintf("10.0.0.1:%d", p.Port)
	}
	f.deployments[p.Name] = d
	return nil
}

func (f *fakeCluster) Stop(name string, purge bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped[name] = true
	if purge {
		f.purged[name] = true
		delete(f.deployments, name)
		return nil
	}
	if d, ok := f.deployments[name]; ok {
		d.Status, d.Healthy = "dead", false
	}
	return nil
}

func (f *fakeCluster) RegisterJob(job *api.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[*job.ID] = 0
	f.jobsSeen++
	f.registered = append(f.registered, job)
	return nil
}

func (f *fakeCluster) BatchStatus(jobID string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.jobs[jobID]
	if !ok {
		return "", "", fmt.Errorf("job %s not found", jobID)
	}
	f.jobs[jobID] = n + 1
	if f.hang || n < 2 {
		return "alloc-" + jobID, "running", nil
	}
	return "alloc-" + jobID, "complete", nil
}

func (f *fakeCluster) ReadLogs(allocID, stream string, all bool) (string, error) {
	if all {
		return sampleResult, nil
	}
	return "PROGRESS load test at concurrency 4\n", nil
}

func (f *fakeCluster) FailureReason(allocID string) string { return "boom\nfinal error line" }

func (f *fakeCluster) PurgeJob(jobID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.jobs, jobID)
	return nil
}

func (f *fakeCluster) PurgeJobsWithPrefix(prefix string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id := range f.jobs {
		if strings.HasPrefix(id, prefix) {
			delete(f.jobs, id)
		}
	}
	return nil
}

func (f *fakeCluster) GPUStats(ctx context.Context) ([]hardware.GPUStats, error) {
	return []hardware.GPUStats{
		{Vendor: "nvidia", VRAMUsed: 4 << 30, UtilPercent: 90},
		{Vendor: "nvidia", VRAMUsed: 2 << 30, UtilPercent: 70},
		{Vendor: "amd", VRAMUsed: 20 << 30, UtilPercent: 5},
	}, nil
}

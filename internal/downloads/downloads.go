// Package downloads manages Hugging Face model downloads, executed as
// dispatched instances of the ams-hf-download batch job on the remote box.
// The list of downloads is derived from Nomad itself (dispatched jobs and
// their meta), so it survives restarts of this application.
package downloads

import (
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-model-scheduler/internal/config"
	"ai-model-scheduler/internal/jobspec"
	"ai-model-scheduler/internal/nomadapi"
)

// Download describes one dispatched download and its current state.
type Download struct {
	ID          string    `json:"id"` // dispatched job ID, contains a slash
	RepoID      string    `json:"repo_id"`
	Dest        string    `json:"dest"`
	Revision    string    `json:"revision,omitempty"`
	Include     string    `json:"include,omitempty"`
	Status      string    `json:"status"` // pending | running | complete | failed
	SubmittedAt time.Time `json:"submitted_at"`
	AllocID     string    `json:"alloc_id,omitempty"`
}

// Service dispatches and tracks downloads.
type Service struct {
	nomad      *nomadapi.Client
	env        jobspec.Env
	hfToken    string
	log        *slog.Logger
	onComplete func() // fired once per download that transitions to complete

	mu   sync.Mutex
	seen map[string]string // dispatched job ID -> last observed status
}

func New(nomad *nomadapi.Client, cfg config.Config, log *slog.Logger, onComplete func()) *Service {
	return &Service{
		nomad:      nomad,
		env:        jobspec.EnvFromConfig(cfg),
		hfToken:    cfg.HFToken,
		log:        log,
		onComplete: onComplete,
		seen:       make(map[string]string),
	}
}

var repoRe = regexp.MustCompile(`^[\w][\w.-]*/[\w][\w.-]*$`)

// Start validates and dispatches a download. dest defaults to the repo ID
// (creating org/name directories on the share, which is what vLLM expects).
func (s *Service) Start(repoID, revision, include, dest string) (string, error) {
	repoID = strings.TrimSpace(repoID)
	if !repoRe.MatchString(repoID) {
		return "", fmt.Errorf("repo id must look like org/name, got %q", repoID)
	}
	if dest == "" {
		dest = repoID
	}
	if strings.Contains(dest, "..") || strings.HasPrefix(dest, "/") {
		return "", fmt.Errorf("invalid destination %q", dest)
	}

	// Upsert the parameterized job so registration never has to happen at
	// startup (and picks up HF token/image changes).
	if err := s.nomad.Register(jobspec.HFDownload(s.env, s.hfToken)); err != nil {
		return "", fmt.Errorf("registering download job: %w", err)
	}

	meta := map[string]string{"repo_id": repoID, "dest": dest}
	if revision != "" {
		meta["revision"] = revision
	}
	if include != "" {
		meta["include"] = include
	}
	id, err := s.nomad.Dispatch(jobspec.HFDownloadJobID, meta)
	if err != nil {
		return "", err
	}
	s.log.Info("download dispatched", "id", id, "repo", repoID, "dest", dest)
	return id, nil
}

// List returns all dispatched downloads known to Nomad, newest first.
func (s *Service) List() ([]Download, error) {
	stubs, err := s.nomad.ListByPrefix(jobspec.HFDownloadJobID + "/")
	if err != nil {
		return nil, err
	}
	out := make([]Download, 0, len(stubs))
	for _, stub := range stubs {
		d := Download{
			ID:          stub.ID,
			Status:      "pending",
			SubmittedAt: time.Unix(0, stub.SubmitTime),
		}
		if meta, err := s.nomad.JobMeta(stub.ID); err == nil {
			d.RepoID = meta["repo_id"]
			d.Dest = meta["dest"]
			d.Revision = meta["revision"]
			d.Include = meta["include"]
		}
		if alloc, err := s.nomad.LatestAlloc(stub.ID); err == nil && alloc != nil {
			d.AllocID = alloc.ID
			d.Status = alloc.ClientStatus
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SubmittedAt.After(out[j].SubmittedAt) })
	s.notifyTransitions(out)
	return out, nil
}

// notifyTransitions fires onComplete once per download newly observed in the
// complete state, so the model catalog can refresh itself.
func (s *Service) notifyTransitions(downloads []Download) {
	if s.onComplete == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range downloads {
		prev, known := s.seen[d.ID]
		s.seen[d.ID] = d.Status
		if d.Status == "complete" && known && prev != "complete" {
			s.log.Info("download complete, refreshing catalog", "id", d.ID, "repo", d.RepoID)
			go s.onComplete()
		}
	}
}

// Logs returns the tail of a download's output (stdout then stderr).
func (s *Service) Logs(id string) (string, error) {
	alloc, err := s.nomad.LatestAlloc(id)
	if err != nil {
		return "", err
	}
	if alloc == nil {
		return "", fmt.Errorf("download %q has no allocation yet", id)
	}
	stdout, _ := s.nomad.TailLogs(alloc.ID, nomadapi.TaskName, "stdout", 4*1024)
	stderr, _ := s.nomad.TailLogs(alloc.ID, nomadapi.TaskName, "stderr", 8*1024)
	var sb strings.Builder
	if stdout != "" {
		sb.WriteString(stdout)
	}
	if stderr != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n--- stderr ---\n")
		}
		sb.WriteString(stderr)
	}
	return sb.String(), nil
}

// Remove purges a dispatched download job from Nomad.
func (s *Service) Remove(id string) error {
	if !strings.HasPrefix(id, jobspec.HFDownloadJobID+"/") {
		return fmt.Errorf("not a download job: %q", id)
	}
	return s.nomad.Stop(id, true)
}

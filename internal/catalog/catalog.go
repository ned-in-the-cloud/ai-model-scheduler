// Package catalog maintains the model index for the NAS share. It dispatches
// the ams-indexer batch job on the remote box, parses the NDJSON manifest the
// job prints to stdout, and caches the result with a TTL.
package catalog

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

// Entry is one model on the share.
type Entry struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"` // gguf | hf-dir
	SizeBytes int64  `json:"size_bytes"`
}

// Catalog caches the model index.
type Catalog struct {
	nomad *nomadapi.Client
	env   jobspec.Env
	ttl   time.Duration
	log   *slog.Logger

	mu      sync.Mutex
	entries []Entry
	fetched time.Time
}

func New(nomad *nomadapi.Client, cfg config.Config, log *slog.Logger) *Catalog {
	return &Catalog{nomad: nomad, env: jobspec.EnvFromConfig(cfg), ttl: cfg.CatalogTTL, log: log}
}

// Cached returns whatever is in the cache without triggering a refresh.
func (c *Catalog) Cached() ([]Entry, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries, c.fetched
}

// Entries returns the cached model list, refreshing it first if the cache is
// empty or older than the TTL.
func (c *Catalog) Entries(ctx context.Context) ([]Entry, time.Time, error) {
	c.mu.Lock()
	fresh := !c.fetched.IsZero() && time.Since(c.fetched) < c.ttl
	entries, fetched := c.entries, c.fetched
	c.mu.Unlock()
	if fresh {
		return entries, fetched, nil
	}
	if err := c.Refresh(ctx); err != nil {
		// Stale data beats no data, but only if we have some.
		if fetched.IsZero() {
			return nil, time.Time{}, err
		}
		c.log.Warn("catalog refresh failed, serving stale data", "err", err)
		return entries, fetched, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries, c.fetched, nil
}

// Refresh dispatches the indexer job, waits for it, and replaces the cache.
func (c *Catalog) Refresh(ctx context.Context) error {
	// Upsert the parameterized job first: self-registration is idempotent
	// and removes any startup-ordering dependency on Nomad availability.
	if err := c.nomad.Register(jobspec.Indexer(c.env)); err != nil {
		return fmt.Errorf("registering indexer job: %w", err)
	}

	dispatchID, err := c.nomad.Dispatch(jobspec.IndexerJobID, nil)
	if err != nil {
		return err
	}
	// Dispatched instances are one-shot; purge them once read.
	defer func() {
		if err := c.nomad.Stop(dispatchID, true); err != nil {
			c.log.Warn("purging indexer dispatch", "id", dispatchID, "err", err)
		}
	}()

	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res, err := c.nomad.WaitForBatch(waitCtx, dispatchID)
	if err != nil {
		return err
	}
	stdout, logErr := c.nomad.ReadAllLogs(res.AllocID, nomadapi.TaskName, "stdout")
	if res.ClientStatus != "complete" {
		return fmt.Errorf("indexer job failed: %s", c.nomad.BatchFailureReason(res.AllocID))
	}
	if logErr != nil && stdout == "" {
		return fmt.Errorf("reading indexer output: %w", logErr)
	}

	entries, err := parseManifest(stdout)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.entries, c.fetched = entries, time.Now()
	c.mu.Unlock()
	c.log.Info("catalog refreshed", "models", len(entries))
	return nil
}

// parseManifest decodes NDJSON output from the indexer, skipping lines that
// are not valid JSON objects (e.g. stray shell noise). A line only needs to
// contain an object, not start with one, so leftover log-driver prefixes
// can't hide entries.
func parseManifest(out string) ([]Entry, error) {
	entries := []Entry{}
	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, "{")
		if i < 0 {
			continue
		}
		line = line[i:]
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if e.Path != "" && e.Kind != "" {
			entries = append(entries, e)
		}
	}
	return entries, nil
}

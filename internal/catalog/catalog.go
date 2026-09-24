// Package catalog maintains the model index for the NAS share. It dispatches
// the ams-indexer batch job on the remote box, parses the NDJSON manifest the
// job prints to stdout, and caches the result with a TTL. Stale cache entries
// are served immediately while a rescan runs in the background.
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

	mu          sync.Mutex
	entries     []Entry
	fetched     time.Time
	inflight    *flight   // non-nil while a rescan is running
	lastAttempt time.Time // start of the most recent rescan, successful or not
	lastErr     error     // error from the most recent rescan
}

// flight is one in-progress rescan that any number of callers can wait on.
type flight struct {
	done chan struct{}
	err  error
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

// Status reports whether a rescan is in progress and the error from the most
// recent one, if it failed.
func (c *Catalog) Status() (refreshing bool, lastErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inflight != nil, c.lastErr
}

// Entries returns the cached model list. An empty cache blocks on a rescan;
// a cache older than the TTL is returned as-is while a rescan starts in the
// background (stale-while-revalidate).
func (c *Catalog) Entries(ctx context.Context) ([]Entry, time.Time, error) {
	c.mu.Lock()
	entries, fetched := c.entries, c.fetched
	if !fetched.IsZero() {
		// Throttle on the last attempt, not the last success, so a failing
		// indexer isn't redispatched on every page load.
		if time.Since(c.lastAttempt) >= c.ttl {
			c.startLocked()
		}
		c.mu.Unlock()
		return entries, fetched, nil
	}
	c.mu.Unlock()

	if err := c.Refresh(ctx); err != nil {
		return nil, time.Time{}, err
	}
	entries, fetched = c.Cached()
	return entries, fetched, nil
}

// Refresh rescans the share and waits for the result. Concurrent callers
// share a single indexer dispatch. The rescan is detached from ctx, so a
// client navigating away doesn't abort it; ctx only bounds the wait.
func (c *Catalog) Refresh(ctx context.Context) error {
	c.mu.Lock()
	f := c.startLocked()
	c.mu.Unlock()
	select {
	case <-f.done:
		return f.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startLocked begins a background rescan unless one is already running, and
// returns the in-progress flight. c.mu must be held.
func (c *Catalog) startLocked() *flight {
	if c.inflight != nil {
		return c.inflight
	}
	f := &flight{done: make(chan struct{})}
	c.inflight, c.lastAttempt = f, time.Now()
	go func() {
		err := c.scan(context.Background())
		if err != nil {
			c.log.Warn("catalog refresh failed", "err", err)
		}
		c.mu.Lock()
		c.inflight, c.lastErr = nil, err
		c.mu.Unlock()
		f.err = err
		close(f.done)
	}()
	return f
}

// scan dispatches the indexer job, waits for it, and replaces the cache.
func (c *Catalog) scan(ctx context.Context) error {
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

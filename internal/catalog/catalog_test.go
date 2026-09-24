package catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-model-scheduler/internal/config"
	"ai-model-scheduler/internal/nomadapi"
)

// fakeIndexerNomad mimics the endpoints a catalog refresh touches: register,
// dispatch, allocation polling, log retrieval, and purge.
type fakeIndexerNomad struct {
	mu         sync.Mutex
	stdout     string
	status     string // client status of the indexer allocation
	dispatches int
	purges     int
}

func (f *fakeIndexerNomad) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := r.URL.Path
		switch {
		case p == "/v1/jobs" && r.Method == http.MethodPut:
			fmt.Fprint(w, `{"EvalID":"e1"}`)
		case strings.HasSuffix(p, "/dispatch"):
			f.dispatches++
			fmt.Fprint(w, `{"DispatchedJobID":"ams-indexer/dispatch-1","EvalID":"e2"}`)
		case strings.HasSuffix(p, "/allocations"):
			fmt.Fprintf(w, `[{"ID":"a-idx","CreateIndex":1,"ClientStatus":%q}]`, f.status)
		case p == "/v1/allocation/a-idx":
			fmt.Fprintf(w, `{"ID":"a-idx","ClientStatus":%q}`, f.status)
		case strings.HasPrefix(p, "/v1/client/fs/logs/"):
			data := f.stdout
			if r.URL.Query().Get("type") == "stderr" {
				data = "boom\n"
			}
			frame := map[string]any{"Offset": len(data), "Data": base64.StdEncoding.EncodeToString([]byte(data))}
			_ = json.NewEncoder(w).Encode(frame)
		case strings.HasPrefix(p, "/v1/job/") && r.Method == http.MethodDelete:
			f.purges++
			fmt.Fprint(w, `{"EvalID":"e3"}`)
		default:
			http.NotFound(w, r)
		}
	})
}

func newTestCatalog(t *testing.T, f *fakeIndexerNomad) *Catalog {
	t.Helper()
	ts := httptest.NewServer(f.handler())
	t.Cleanup(ts.Close)
	nc, err := nomadapi.New(ts.URL, "")
	if err != nil {
		t.Fatalf("nomadapi.New: %v", err)
	}
	cfg := config.Config{
		NomadAddr: ts.URL, Driver: "docker",
		ModelRootHost: "/mnt/models", ModelMount: "/models",
		CatalogTTL: time.Minute,
	}
	return New(nc, cfg, slog.New(slog.DiscardHandler))
}

func TestRefreshParsesManifest(t *testing.T) {
	f := &fakeIndexerNomad{
		status: "complete",
		stdout: `{"kind":"gguf","path":"llama3-q4.gguf","size_bytes":4000000000}
{"kind":"hf-dir","path":"qwen2-7b","size_bytes":15000000000}
not json noise
`,
	}
	c := newTestCatalog(t, f)

	entries, fetched, err := c.Entries(context.Background())
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}
	if entries[0].Kind != "gguf" || entries[0].Path != "llama3-q4.gguf" || entries[0].SizeBytes != 4000000000 {
		t.Errorf("entry 0: %+v", entries[0])
	}
	if entries[1].Kind != "hf-dir" || entries[1].Path != "qwen2-7b" {
		t.Errorf("entry 1: %+v", entries[1])
	}
	if fetched.IsZero() {
		t.Error("fetched time not set")
	}
	if f.purges != 1 {
		t.Errorf("dispatched job purges = %d, want 1", f.purges)
	}

	// Second call within TTL must serve from cache without a new dispatch.
	if _, _, err := c.Entries(context.Background()); err != nil {
		t.Fatalf("cached Entries: %v", err)
	}
	if f.dispatches != 1 {
		t.Errorf("dispatches = %d, want 1 (cache hit expected)", f.dispatches)
	}
}

func TestRefreshParsesPodmanPrefixedManifest(t *testing.T) {
	// podman's k8s-file log driver prefixes every line; the nomadapi layer
	// strips it, and the parser must still see the JSON.
	f := &fakeIndexerNomad{
		status: "complete",
		stdout: `2026-09-17T18:00:00.000000001+00:00 stdout F {"kind":"gguf","path":"llama3-q4.gguf","size_bytes":4000000000}
2026-09-17T18:00:00.000000002+00:00 stdout F {"kind":"hf-dir","path":"qwen2-7b","size_bytes":15000000000}
`,
	}
	c := newTestCatalog(t, f)

	entries, _, err := c.Entries(context.Background())
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}
	if entries[0].Path != "llama3-q4.gguf" || entries[1].Path != "qwen2-7b" {
		t.Errorf("entries: %+v", entries)
	}
}

func TestRefreshFailedJobSurfacesStderr(t *testing.T) {
	f := &fakeIndexerNomad{status: "failed", stdout: ""}
	c := newTestCatalog(t, f)

	_, _, err := c.Entries(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want indexer failure with stderr", err)
	}
}

func (f *fakeIndexerNomad) counts() (dispatches, purges int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dispatches, f.purges
}

func TestStaleCacheServedWhileRescanning(t *testing.T) {
	f := &fakeIndexerNomad{status: "complete", stdout: `{"kind":"gguf","path":"old.gguf","size_bytes":1}` + "\n"}
	c := newTestCatalog(t, f)
	if _, _, err := c.Entries(context.Background()); err != nil {
		t.Fatalf("Entries: %v", err)
	}

	// Age the cache past the TTL and change what the share holds.
	c.mu.Lock()
	c.fetched = c.fetched.Add(-2 * c.ttl)
	c.lastAttempt = c.fetched
	staleAt := c.fetched
	c.mu.Unlock()
	f.mu.Lock()
	f.stdout = `{"kind":"gguf","path":"new.gguf","size_bytes":1}` + "\n"
	f.mu.Unlock()

	entries, fetched, err := c.Entries(context.Background())
	if err != nil {
		t.Fatalf("stale Entries: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "old.gguf" || !fetched.Equal(staleAt) {
		t.Fatalf("expected stale cache to be served immediately, got %+v at %v", entries, fetched)
	}

	// Wait for the background rescan to land.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if refreshing, _ := c.Status(); !refreshing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background rescan did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	entries, _ = c.Cached()
	if len(entries) != 1 || entries[0].Path != "new.gguf" {
		t.Errorf("after rescan: %+v", entries)
	}
	if d, _ := f.counts(); d != 2 {
		t.Errorf("dispatches = %d, want 2", d)
	}
}

func TestConcurrentRefreshesShareOneDispatch(t *testing.T) {
	f := &fakeIndexerNomad{status: "complete", stdout: `{"kind":"gguf","path":"a.gguf","size_bytes":1}` + "\n"}
	c := newTestCatalog(t, f)

	// Hold the fake so the first dispatch can't complete until every caller
	// has joined the flight.
	f.mu.Lock()
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- c.Refresh(context.Background())
		}()
	}
	time.Sleep(50 * time.Millisecond)
	f.mu.Unlock()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Refresh: %v", err)
		}
	}
	if d, _ := f.counts(); d != 1 {
		t.Errorf("dispatches = %d, want 1", d)
	}
}

func TestParseManifestEmpty(t *testing.T) {
	entries, err := parseManifest("")
	if err != nil || len(entries) != 0 {
		t.Fatalf("parseManifest(\"\") = %v, %v", entries, err)
	}
}

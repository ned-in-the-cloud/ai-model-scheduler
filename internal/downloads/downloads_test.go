package downloads

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-model-scheduler/internal/config"
	"ai-model-scheduler/internal/nomadapi"
)

type fakeDownloadNomad struct {
	registered   atomic.Int32
	dispatchMeta atomic.Value // map[string]string
	allocStatus  atomic.Value // string
	purged       atomic.Value // string: purged job ID
}

func (f *fakeDownloadNomad) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		status, _ := f.allocStatus.Load().(string)
		switch {
		case p == "/v1/jobs" && r.Method == http.MethodPut:
			f.registered.Add(1)
			fmt.Fprint(w, `{"EvalID":"e1"}`)
		case p == "/v1/jobs" && r.Method == http.MethodGet:
			fmt.Fprintf(w, `[{"ID":"ams-hf-download/dispatch-1","Status":"running","SubmitTime":%d}]`,
				time.Now().UnixNano())
		case strings.HasSuffix(p, "/dispatch"):
			var req struct{ Meta map[string]string }
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.dispatchMeta.Store(req.Meta)
			fmt.Fprint(w, `{"DispatchedJobID":"ams-hf-download/dispatch-1","EvalID":"e2"}`)
		case p == "/v1/job/ams-hf-download/dispatch-1" && r.Method == http.MethodGet:
			fmt.Fprint(w, `{"ID":"ams-hf-download/dispatch-1","Meta":{"repo_id":"org/model","dest":"org/model","include":"*.gguf"}}`)
		case strings.HasSuffix(p, "/allocations"):
			fmt.Fprintf(w, `[{"ID":"a-dl","CreateIndex":3,"ClientStatus":%q}]`, status)
		case p == "/v1/allocation/a-dl":
			fmt.Fprintf(w, `{"ID":"a-dl","ClientStatus":%q}`, status)
		case r.Method == http.MethodDelete:
			f.purged.Store(strings.TrimPrefix(p, "/v1/job/"))
			fmt.Fprint(w, `{"EvalID":"e3"}`)
		default:
			http.NotFound(w, r)
		}
	})
}

func newTestService(t *testing.T, f *fakeDownloadNomad, onComplete func()) *Service {
	t.Helper()
	ts := httptest.NewServer(f.handler())
	t.Cleanup(ts.Close)
	nc, err := nomadapi.New(ts.URL, "")
	if err != nil {
		t.Fatalf("nomadapi.New: %v", err)
	}
	cfg := config.Config{
		NomadAddr: ts.URL, Driver: "docker",
		ModelRootHost: "/mnt/models", ModelMount: "/models", HFToken: "hf_test",
	}
	return New(nc, cfg, slog.New(slog.DiscardHandler), onComplete)
}

func TestStartValidation(t *testing.T) {
	tests := []struct {
		name    string
		repo    string
		dest    string
		wantErr string
	}{
		{name: "valid", repo: "org/model"},
		{name: "missing org", repo: "model", wantErr: "org/name"},
		{name: "empty", repo: "", wantErr: "org/name"},
		{name: "path traversal dest", repo: "org/model", dest: "../evil", wantErr: "destination"},
		{name: "absolute dest", repo: "org/model", dest: "/etc", wantErr: "destination"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeDownloadNomad{}
			svc := newTestService(t, f, nil)
			id, err := svc.Start(tt.repo, "", "", tt.dest)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				if f.dispatchMeta.Load() != nil {
					t.Error("dispatch should not happen on validation failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if id != "ams-hf-download/dispatch-1" {
				t.Errorf("id = %q", id)
			}
		})
	}
}

func TestStartDispatchMeta(t *testing.T) {
	f := &fakeDownloadNomad{}
	svc := newTestService(t, f, nil)
	if _, err := svc.Start("org/model", "main", "*.gguf", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if f.registered.Load() == 0 {
		t.Error("parameterized job was not upserted before dispatch")
	}
	meta := f.dispatchMeta.Load().(map[string]string)
	want := map[string]string{
		"repo_id": "org/model", "dest": "org/model",
		"revision": "main", "include": "*.gguf",
	}
	for k, v := range want {
		if meta[k] != v {
			t.Errorf("meta[%s] = %q, want %q", k, meta[k], v)
		}
	}
}

func TestListAndCompletionCallback(t *testing.T) {
	f := &fakeDownloadNomad{}
	f.allocStatus.Store("running")
	var completions atomic.Int32
	done := make(chan struct{}, 4)
	svc := newTestService(t, f, func() {
		completions.Add(1)
		done <- struct{}{}
	})

	list, err := svc.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d downloads, want 1", len(list))
	}
	d := list[0]
	if d.RepoID != "org/model" || d.Include != "*.gguf" || d.Status != "running" {
		t.Errorf("download: %+v", d)
	}
	if completions.Load() != 0 {
		t.Error("callback fired while still running")
	}

	// Transition to complete: exactly one callback despite repeated Lists.
	f.allocStatus.Store("complete")
	if _, err := svc.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("completion callback never fired")
	}
	if _, err := svc.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := completions.Load(); got != 1 {
		t.Errorf("completions = %d, want 1", got)
	}
}

func TestRemove(t *testing.T) {
	f := &fakeDownloadNomad{}
	svc := newTestService(t, f, nil)

	if err := svc.Remove("model-llama3"); err == nil {
		t.Error("Remove should refuse non-download job IDs")
	}
	if err := svc.Remove("ams-hf-download/dispatch-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got, _ := f.purged.Load().(string); got != "ams-hf-download/dispatch-1" {
		t.Errorf("purged = %q", got)
	}
}

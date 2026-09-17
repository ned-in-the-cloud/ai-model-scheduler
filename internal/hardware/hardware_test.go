package hardware

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-model-scheduler/internal/config"
	"ai-model-scheduler/internal/nomadapi"
)

type fakeProbeNomad struct {
	stdout     string
	status     string
	dispatches int
}

func (f *fakeProbeNomad) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/v1/jobs" && r.Method == http.MethodPut:
			fmt.Fprint(w, `{"EvalID":"e1"}`)
		case strings.HasSuffix(p, "/dispatch"):
			f.dispatches++
			fmt.Fprint(w, `{"DispatchedJobID":"ams-gpu-probe/dispatch-1","EvalID":"e2"}`)
		case strings.HasSuffix(p, "/allocations"):
			fmt.Fprintf(w, `[{"ID":"a-gpu","CreateIndex":1,"ClientStatus":%q}]`, f.status)
		case p == "/v1/allocation/a-gpu":
			fmt.Fprintf(w, `{"ID":"a-gpu","ClientStatus":%q}`, f.status)
		case strings.HasPrefix(p, "/v1/client/fs/logs/"):
			data := f.stdout
			if r.URL.Query().Get("type") == "stderr" {
				data = ""
			}
			frame := map[string]any{"Data": base64.StdEncoding.EncodeToString([]byte(data))}
			_ = json.NewEncoder(w).Encode(frame)
		case r.Method == http.MethodDelete:
			fmt.Fprint(w, `{"EvalID":"e3"}`)
		default:
			http.NotFound(w, r)
		}
	})
}

func newTestService(t *testing.T, f *fakeProbeNomad) *Service {
	t.Helper()
	ts := httptest.NewServer(f.handler())
	t.Cleanup(ts.Close)
	nc, err := nomadapi.New(ts.URL, "")
	if err != nil {
		t.Fatalf("nomadapi.New: %v", err)
	}
	cfg := config.Config{NomadAddr: ts.URL, Driver: "docker", ModelRootHost: "/mnt/models", ModelMount: "/models"}
	return New(nc, cfg, slog.New(slog.DiscardHandler))
}

func TestProbeParsesGPUs(t *testing.T) {
	f := &fakeProbeNomad{
		status: "complete",
		stdout: `{"index":0,"vendor":"amd","name":"AMD Radeon R9700","pci":"0000:03:00.0"}
{"index":1,"vendor":"nvidia","name":"NVIDIA GeForce RTX 3090","pci":"0000:01:00.0"}
{"index":2,"vendor":"other","name":"ASPEED BMC","pci":"0000:05:00.0"}
noise line
`,
	}
	svc := newTestService(t, f)

	gpus, fetched, err := svc.Entries(context.Background())
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(gpus) != 3 {
		t.Fatalf("got %d gpus, want 3: %+v", len(gpus), gpus)
	}
	if gpus[0].Vendor != "amd" || gpus[0].Name != "AMD Radeon R9700" {
		t.Errorf("gpu 0: %+v", gpus[0])
	}
	if !gpus[0].Deployable() || !gpus[1].Deployable() || gpus[2].Deployable() {
		t.Errorf("deployable flags wrong: %+v", gpus)
	}
	if fetched.IsZero() {
		t.Error("fetched time not set")
	}

	// Cached within TTL: no second dispatch.
	if _, _, err := svc.Entries(context.Background()); err != nil {
		t.Fatalf("cached Entries: %v", err)
	}
	if f.dispatches != 1 {
		t.Errorf("dispatches = %d, want 1", f.dispatches)
	}
}

func TestProbePodmanPrefixedOutput(t *testing.T) {
	f := &fakeProbeNomad{
		status: "complete",
		stdout: `2026-09-17T18:00:00.000000001+00:00 stdout F {"index":0,"vendor":"intel","name":"Intel Arc B70","pci":"0000:03:00.0"}` + "\n",
	}
	svc := newTestService(t, f)
	gpus, _, err := svc.Entries(context.Background())
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(gpus) != 1 || gpus[0].Vendor != "intel" {
		t.Fatalf("gpus = %+v", gpus)
	}
}

func TestProbeFailureSurfaced(t *testing.T) {
	f := &fakeProbeNomad{status: "failed"}
	svc := newTestService(t, f)
	_, _, err := svc.Entries(context.Background())
	if err == nil || !strings.Contains(err.Error(), "gpu probe failed") {
		t.Fatalf("err = %v, want probe failure", err)
	}
}

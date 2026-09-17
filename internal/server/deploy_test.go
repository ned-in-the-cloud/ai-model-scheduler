package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// deployableNomadMux extends the cluster mux with register/deregister
// endpoints, recording what was submitted.
type registered struct {
	count atomic.Int32
	last  atomic.Value // map[string]any of the submitted job
}

func deployableNomadMux(reg *registered) *http.ServeMux {
	mux := clusterNomadMux()
	mux.HandleFunc("PUT /v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Job map[string]any
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		reg.count.Add(1)
		reg.last.Store(body.Job)
		_, _ = w.Write([]byte(`{"EvalID":"eval1"}`))
	})
	mux.HandleFunc("DELETE /v1/job/model-llama3", func(w http.ResponseWriter, r *http.Request) {
		reg.count.Add(1)
		reg.last.Store(map[string]any{"deregistered": r.URL.Query().Get("purge")})
		_, _ = w.Write([]byte(`{"EvalID":"eval2"}`))
	})
	return mux
}

func TestCreateDeployment(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantStatus   int
		wantErrPart  string
		wantRegister bool
	}{
		{
			name:         "valid llamacpp deploy",
			body:         `{"name":"phi4","runtime":"llamacpp","model":"phi4.gguf","port":8010}`,
			wantStatus:   http.StatusCreated,
			wantRegister: true,
		},
		{
			name:         "port auto-suggested when omitted",
			body:         `{"name":"phi4","runtime":"llamacpp","model":"phi4.gguf"}`,
			wantStatus:   http.StatusCreated,
			wantRegister: true,
		},
		{
			name:        "duplicate name",
			body:        `{"name":"llama3","runtime":"llamacpp","model":"x.gguf","port":8010}`,
			wantStatus:  http.StatusConflict,
			wantErrPart: "already exists",
		},
		{
			name:        "port collision with running deployment",
			body:        `{"name":"phi4","runtime":"llamacpp","model":"x.gguf","port":8001}`,
			wantStatus:  http.StatusConflict,
			wantErrPart: "already used",
		},
		{
			name:        "port out of range",
			body:        `{"name":"phi4","runtime":"llamacpp","model":"x.gguf","port":9500}`,
			wantStatus:  http.StatusBadRequest,
			wantErrPart: "outside allowed range",
		},
		{
			name:        "invalid name",
			body:        `{"name":"Bad Name","runtime":"llamacpp","model":"x.gguf","port":8010}`,
			wantStatus:  http.StatusBadRequest,
			wantErrPart: "name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reg registered
			srv := newTestServer(t, fakeNomad(t, deployableNomadMux(&reg)).URL, nil)
			req := httptest.NewRequest("POST", "/api/v1/deployments", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.wantErrPart != "" && !strings.Contains(rec.Body.String(), tt.wantErrPart) {
				t.Errorf("body %q missing %q", rec.Body, tt.wantErrPart)
			}
			if got := reg.count.Load() > 0; got != tt.wantRegister {
				t.Errorf("job registered = %v, want %v", got, tt.wantRegister)
			}
		})
	}
}

func TestCreateDeploymentSubmitsExpectedJob(t *testing.T) {
	var reg registered
	srv := newTestServer(t, fakeNomad(t, deployableNomadMux(&reg)).URL, nil)
	body := `{"name":"phi4","runtime":"llamacpp","model":"phi4.gguf"}`
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/deployments", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body)
	}

	job := reg.last.Load().(map[string]any)
	if job["ID"] != "model-phi4" {
		t.Errorf("job ID = %v", job["ID"])
	}
	// Suggested port must skip 8001 (used by the running model-llama3) and
	// land on the first free port, 8000.
	b, _ := json.Marshal(job)
	if !strings.Contains(string(b), `"Value":8000`) {
		t.Errorf("expected suggested port 8000 in job: %s", b)
	}
}

func TestDeleteDeployment(t *testing.T) {
	var reg registered
	srv := newTestServer(t, fakeNomad(t, deployableNomadMux(&reg)).URL, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/v1/deployments/llama3", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body)
	}
	if reg.count.Load() != 1 {
		t.Errorf("deregister calls = %d, want 1", reg.count.Load())
	}
}

func TestDeleteUnknownDeployment(t *testing.T) {
	var reg registered
	srv := newTestServer(t, fakeNomad(t, deployableNomadMux(&reg)).URL, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/v1/deployments/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if reg.count.Load() != 0 {
		t.Errorf("deregister should not be called for unknown deployment")
	}
}

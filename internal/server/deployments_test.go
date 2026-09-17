package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-model-scheduler/internal/nomadapi"
)

// clusterNomadMux extends the healthy mux with one managed inference job, one
// foreign job, and a ready node with stats.
func clusterNomadMux() *http.ServeMux {
	mux := healthyNomadMux()

	mux.HandleFunc("/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"ID":"model-llama3","Status":"running"},
			{"ID":"model-old","Status":"dead"},
			{"ID":"model-rogue","Status":"running"},
			{"ID":"unrelated-job","Status":"running"}
		]`))
	})
	mux.HandleFunc("/v1/job/model-old", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"ID":"model-old",
			"Meta":{
				"managed-by":"ai-model-scheduler","kind":"inference",
				"runtime":"llamacpp","model":"old.gguf","gpu":"nvidia",
				"params":"{\"name\":\"old\",\"runtime\":\"llamacpp\",\"model\":\"old.gguf\",\"port\":8005,\"gpu\":\"nvidia\",\"ctx_size\":16384,\"extra_args\":\"--flash-attn\"}"
			},
			"TaskGroups":[{"Tasks":[{"Resources":{"CPU":1500,"MemoryMB":8192}}]}]
		}`))
	})
	mux.HandleFunc("/v1/job/model-old/allocations", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/v1/job/model-llama3", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"ID":"model-llama3",
			"Meta":{"managed-by":"ai-model-scheduler","kind":"inference","runtime":"llamacpp","model":"llama3-q4.gguf","gpu":"false"},
			"TaskGroups":[{"Tasks":[{"Resources":{"CPU":2000,"MemoryMB":4096}}]}]
		}`))
	})
	// A job that matches the name prefix but is not tagged as ours.
	mux.HandleFunc("/v1/job/model-rogue", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ID":"model-rogue","Meta":{}}`))
	})
	mux.HandleFunc("/v1/job/model-llama3/allocations", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"ID":"alloc-old","CreateIndex":5,"ClientStatus":"complete"},
			{"ID":"alloc-new","CreateIndex":9,"ClientStatus":"running"}
		]`))
	})
	mux.HandleFunc("/v1/allocation/alloc-new", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"ID":"alloc-new","ClientStatus":"running",
			"AllocatedResources":{"Shared":{"Ports":[{"Label":"api","Value":8001,"HostIP":"10.0.0.5"}]}}
		}`))
	})
	mux.HandleFunc("/v1/client/allocation/alloc-new/stats", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"ResourceUsage":{"CpuStats":{"TotalTicks":850.5},"MemoryStats":{"RSS":3221225472}}
		}`))
	})
	mux.HandleFunc("/v1/node/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ID":"n1","Name":"inference-box","Status":"ready"}`))
	})
	mux.HandleFunc("/v1/client/stats", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"CPU":[{"CPU":"cpu0","Idle":80},{"CPU":"cpu1","Idle":60}],
			"Memory":{"Total":16000000000,"Used":8000000000},
			"Uptime":3600
		}`))
	})
	return mux
}

func TestListDeployments(t *testing.T) {
	srv := newTestServer(t, fakeNomad(t, clusterNomadMux()).URL, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/deployments", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body)
	}
	var deps []nomadapi.Deployment
	if err := json.Unmarshal(rec.Body.Bytes(), &deps); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(deps) != 2 {
		t.Fatalf("got %d deployments, want 2 (untagged and foreign jobs excluded): %+v", len(deps), deps)
	}
	d := deps[0]
	if d.Name != "llama3" || d.Runtime != "llamacpp" || d.Model != "llama3-q4.gguf" {
		t.Errorf("unexpected deployment identity: %+v", d)
	}
	if !d.Healthy || d.AllocID != "alloc-new" {
		t.Errorf("latest alloc not resolved: %+v", d)
	}
	if d.Endpoint != "10.0.0.5:8001" || d.Port != 8001 {
		t.Errorf("endpoint = %q port = %d, want 10.0.0.5:8001", d.Endpoint, d.Port)
	}
	if d.CPUMHz != 2000 || d.MemMB != 4096 {
		t.Errorf("resources = %d MHz / %d MB", d.CPUMHz, d.MemMB)
	}
	if d.UsageCPUMHz != 850.5 || d.UsageMemBytes != 3221225472 {
		t.Errorf("usage = %v MHz / %d bytes, want 850.5 / 3221225472", d.UsageCPUMHz, d.UsageMemBytes)
	}
}

func TestDeploymentsPartial(t *testing.T) {
	srv := newTestServer(t, fakeNomad(t, clusterNomadMux()).URL, nil)

	t.Run("running tab", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/partials/deployments?filter=running", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"llama3", "10.0.0.5:8001", "Running (1)", "Stopped (1)"} {
			if !strings.Contains(body, want) {
				t.Errorf("partial missing %q; body: %s", want, body)
			}
		}
		if strings.Contains(body, "model-old") || strings.Contains(body, "Relaunch") {
			t.Errorf("running tab leaked stopped rows; body: %s", body)
		}
	})

	t.Run("stopped tab", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/partials/deployments?filter=stopped", nil))
		body := rec.Body.String()
		for _, want := range []string{"old", "Relaunch", "Remove", "purge=1"} {
			if !strings.Contains(body, want) {
				t.Errorf("stopped tab missing %q; body: %s", want, body)
			}
		}
		if strings.Contains(body, "10.0.0.5:8001") {
			t.Errorf("stopped tab leaked running rows; body: %s", body)
		}
	})
}

func TestDeploymentsTabCookie(t *testing.T) {
	srv := newTestServer(t, fakeNomad(t, clusterNomadMux()).URL, nil)

	// Explicit filter sets the cookie.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/partials/deployments?filter=stopped", nil))
	var cookie string
	for _, c := range rec.Result().Cookies() {
		if c.Name == "ams-deploy-tab" {
			cookie = c.Value
		}
	}
	if cookie != "stopped" {
		t.Fatalf("tab cookie = %q, want stopped", cookie)
	}

	// No filter + cookie: renders the remembered tab.
	req := httptest.NewRequest("GET", "/partials/deployments", nil)
	req.AddCookie(&http.Cookie{Name: "ams-deploy-tab", Value: "stopped"})
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Relaunch") {
		t.Errorf("cookie-selected stopped tab not rendered; body: %.300s", rec.Body.String())
	}
}

func TestParseEnvLines(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    map[string]string
		wantErr bool
	}{
		{name: "empty", in: "  \n\n"},
		{
			name: "two vars with blank line",
			in:   "HSA_OVERRIDE_GFX_VERSION=12.0.1\n\nHF_TOKEN=abc",
			want: map[string]string{"HSA_OVERRIDE_GFX_VERSION": "12.0.1", "HF_TOKEN": "abc"},
		},
		{name: "value with equals", in: "A=b=c", want: map[string]string{"A": "b=c"}},
		{name: "missing equals", in: "JUSTAKEY", wantErr: true},
		{name: "empty key", in: "=value", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseEnvLines(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && len(tt.want) > 0 {
				for k, v := range tt.want {
					if got[k] != v {
						t.Errorf("env[%s] = %q, want %q", k, got[k], v)
					}
				}
			}
		})
	}
}

func TestRelaunchPrefill(t *testing.T) {
	srv := newTestServer(t, fakeNomad(t, clusterNomadMux()).URL, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/deploy?from=old", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	// Values stored in the params meta must land back in the form.
	for _, want := range []string{
		`value="old"`, `value="old.gguf"`, `value="8005"`,
		`value="16384"`, `value="--flash-attn"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("form missing prefill %s", want)
		}
	}
	if !strings.Contains(body, "gpu=nvidia") {
		t.Errorf("gpu fragment not seeded with stored vendor; body: %.500s", body)
	}
}

func TestNodeStats(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status/leader", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`"127.0.0.1:4647"`))
	})
	mux.HandleFunc("/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"ID":"n1","Name":"inference-box","Status":"ready"}]`))
	})
	mux.HandleFunc("/v1/client/stats", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"CPU":[{"CPU":"cpu0","Idle":80},{"CPU":"cpu1","Idle":60}],
			"Memory":{"Total":16000000000,"Used":8000000000},
			"Uptime":3600
		}`))
	})

	srv := newTestServer(t, fakeNomad(t, mux).URL, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/stats/node", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body)
	}
	var stats []nomadapi.NodeStats
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("got %d nodes, want 1", len(stats))
	}
	ns := stats[0]
	if ns.NodeName != "inference-box" {
		t.Errorf("node name = %q", ns.NodeName)
	}
	// cpu0 busy 20%, cpu1 busy 40% -> average 30%
	if ns.CPUPercent < 29.9 || ns.CPUPercent > 30.1 {
		t.Errorf("CPUPercent = %v, want 30", ns.CPUPercent)
	}
	if ns.MemPercent != 50 {
		t.Errorf("MemPercent = %v, want 50", ns.MemPercent)
	}
}

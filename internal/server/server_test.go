package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-model-scheduler/internal/config"
	"ai-model-scheduler/internal/nomadapi"
)

// fakeNomad returns an httptest server that mimics the handful of Nomad API
// endpoints the app touches, driven by a mux the test can extend.
func fakeNomad(t *testing.T, mux *http.ServeMux) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func healthyNomadMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status/leader", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode("127.0.0.1:4647")
	})
	mux.HandleFunc("/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("[]"))
	})
	return mux
}

func newTestServer(t *testing.T, nomadURL string, mutate func(*config.Config)) *Server {
	t.Helper()
	cfg := config.Config{
		NomadAddr:  nomadURL,
		Driver:     "docker",
		ListenAddr: ":0",
		PortMin:    8000,
		PortMax:    8999,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	nc, err := nomadapi.New(cfg.NomadAddr, cfg.NomadToken)
	if err != nil {
		t.Fatalf("nomadapi.New: %v", err)
	}
	srv, err := New(cfg, nc, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return srv
}

func TestAPIHealth(t *testing.T) {
	tests := []struct {
		name          string
		nomad         func(t *testing.T) string // returns nomad URL
		wantStatus    int
		wantReachable bool
	}{
		{
			name: "healthy cluster",
			nomad: func(t *testing.T) string {
				return fakeNomad(t, healthyNomadMux()).URL
			},
			wantStatus:    http.StatusOK,
			wantReachable: true,
		},
		{
			name: "unreachable cluster",
			nomad: func(t *testing.T) string {
				ts := fakeNomad(t, http.NewServeMux())
				url := ts.URL
				ts.Close()
				return url
			},
			wantStatus:    http.StatusServiceUnavailable,
			wantReachable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, tt.nomad(t), nil)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/health", nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tt.wantStatus, rec.Body)
			}
			var h nomadapi.Health
			if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
				t.Fatalf("decoding body: %v", err)
			}
			if h.Reachable != tt.wantReachable {
				t.Errorf("reachable = %v, want %v", h.Reachable, tt.wantReachable)
			}
		})
	}
}

func TestDashboardRenders(t *testing.T) {
	srv := newTestServer(t, fakeNomad(t, healthyNomadMux()).URL, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "AI Model Scheduler") {
		t.Errorf("dashboard missing brand; body: %.200s", body)
	}
}

func TestBasicAuth(t *testing.T) {
	srv := newTestServer(t, fakeNomad(t, healthyNomadMux()).URL, func(c *config.Config) {
		c.BasicAuthUser = "ned"
		c.BasicAuthPass = "secret"
	})

	tests := []struct {
		name       string
		user, pass string
		auth       bool
		wantStatus int
	}{
		{name: "no credentials", wantStatus: http.StatusUnauthorized},
		{name: "wrong password", auth: true, user: "ned", pass: "nope", wantStatus: http.StatusUnauthorized},
		{name: "correct credentials", auth: true, user: "ned", pass: "secret", wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if tt.auth {
				req.SetBasicAuth(tt.user, tt.pass)
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestHealthPartial(t *testing.T) {
	srv := newTestServer(t, fakeNomad(t, healthyNomadMux()).URL, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/partials/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Nomad ✓") {
		t.Errorf("partial missing healthy badge; body: %s", rec.Body)
	}
}

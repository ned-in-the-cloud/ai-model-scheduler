package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.NomadAddr != "http://127.0.0.1:4646" {
		t.Errorf("NomadAddr = %q", cfg.NomadAddr)
	}
	if cfg.PortMin != 8000 || cfg.PortMax != 8999 {
		t.Errorf("port range = %d-%d, want 8000-8999", cfg.PortMin, cfg.PortMax)
	}
	if cfg.Driver != "podman" {
		t.Errorf("Driver = %q, want podman", cfg.Driver)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("NOMAD_ADDR", "http://10.0.0.5:4646")
	t.Setenv("NOMAD_DRIVER", "docker")
	t.Setenv("PORT_MIN", "8100")
	t.Setenv("CATALOG_TTL", "30s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.NomadAddr != "http://10.0.0.5:4646" {
		t.Errorf("NomadAddr = %q", cfg.NomadAddr)
	}
	if cfg.Driver != "docker" {
		t.Errorf("Driver = %q", cfg.Driver)
	}
	if cfg.PortMin != 8100 {
		t.Errorf("PortMin = %d", cfg.PortMin)
	}
	if cfg.CatalogTTL != 30*time.Second {
		t.Errorf("CatalogTTL = %v", cfg.CatalogTTL)
	}
}

func TestValidation(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
	}{
		{name: "bad driver", env: map[string]string{"NOMAD_DRIVER": "containerd"}, wantErr: true},
		{name: "auth user without pass", env: map[string]string{"BASIC_AUTH_USER": "ned"}, wantErr: true},
		{name: "auth pair ok", env: map[string]string{"BASIC_AUTH_USER": "ned", "BASIC_AUTH_PASS": "pw"}, wantErr: false},
		{name: "inverted port range", env: map[string]string{"PORT_MIN": "9000", "PORT_MAX": "8000"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			if (err != nil) != tt.wantErr {
				t.Errorf("Load() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

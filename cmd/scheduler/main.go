// Command scheduler runs the AI model scheduler web UI.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"ai-model-scheduler/internal/config"
	"ai-model-scheduler/internal/nomadapi"
	"ai-model-scheduler/internal/server"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("loading config", "err", err)
		os.Exit(1)
	}

	nomad, err := nomadapi.New(cfg.NomadAddr, cfg.NomadToken)
	if err != nil {
		log.Error("creating nomad client", "err", err)
		os.Exit(1)
	}

	srv, err := server.New(cfg, nomad, log)
	if err != nil {
		log.Error("creating server", "err", err)
		os.Exit(1)
	}

	log.Info("listening", "addr", cfg.ListenAddr, "nomad", cfg.NomadAddr, "driver", cfg.Driver)
	if err := http.ListenAndServe(cfg.ListenAddr, srv.Handler()); err != nil {
		log.Error("server exited", "err", err)
		os.Exit(1)
	}
}

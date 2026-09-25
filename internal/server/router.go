package server

import (
	"net/http"

	"ai-model-scheduler/web"
)

// Handler returns the root http.Handler with all routes and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// JSON API
	mux.HandleFunc("GET /api/v1/health", s.apiHealth)
	mux.HandleFunc("GET /api/v1/deployments", s.apiListDeployments)
	mux.HandleFunc("POST /api/v1/deployments", s.apiCreateDeployment)
	mux.HandleFunc("GET /api/v1/deployments/{name}", s.apiGetDeployment)
	mux.HandleFunc("DELETE /api/v1/deployments/{name}", s.apiDeleteDeployment)
	mux.HandleFunc("GET /api/v1/deployments/{name}/logs", s.apiDeploymentLogs)
	mux.HandleFunc("GET /api/v1/stats/node", s.apiNodeStats)
	mux.HandleFunc("GET /api/v1/stats/allocations/{id}", s.apiAllocStats)
	mux.HandleFunc("GET /api/v1/stats/gpus", s.apiGPUStats)
	mux.HandleFunc("GET /api/v1/hardware/gpus", s.apiListGPUs)
	mux.HandleFunc("POST /api/v1/hardware/refresh", s.apiRefreshGPUs)
	mux.HandleFunc("GET /api/v1/models", s.apiListModels)
	mux.HandleFunc("POST /api/v1/models/refresh", s.apiRefreshModels)
	mux.HandleFunc("GET /api/v1/downloads", s.apiListDownloads)
	mux.HandleFunc("POST /api/v1/downloads", s.apiStartDownload)
	mux.HandleFunc("GET /api/v1/downloads/logs/{id...}", s.apiDownloadLogs)
	mux.HandleFunc("DELETE /api/v1/downloads/{id...}", s.apiDeleteDownload)
	mux.HandleFunc("GET /api/v1/benchmarks/suites", s.apiListSuites)
	mux.HandleFunc("POST /api/v1/benchmarks/suites", s.apiCreateSuite)
	mux.HandleFunc("GET /api/v1/benchmarks/suites/{id}", s.apiGetSuite)
	mux.HandleFunc("DELETE /api/v1/benchmarks/suites/{id}", s.apiDeleteSuite)
	mux.HandleFunc("GET /api/v1/benchmarks/runs", s.apiListRuns)
	mux.HandleFunc("POST /api/v1/benchmarks/runs", s.apiStartRun)
	mux.HandleFunc("GET /api/v1/benchmarks/runs/{id}", s.apiGetRun)
	mux.HandleFunc("DELETE /api/v1/benchmarks/runs/{id}", s.apiDeleteRun)
	mux.HandleFunc("POST /api/v1/benchmarks/runs/{id}/cancel", s.apiCancelRun)
	mux.HandleFunc("GET /api/v1/benchmarks/runs/{id}/export/{format}", s.apiExportRun)
	mux.HandleFunc("GET /api/v1/benchmarks/datasets", s.apiListDatasets)
	mux.HandleFunc("POST /api/v1/benchmarks/datasets", s.apiUploadDataset)
	mux.HandleFunc("DELETE /api/v1/benchmarks/datasets/{id}", s.apiDeleteDataset)

	// UI pages
	mux.HandleFunc("GET /{$}", s.uiDashboard)
	mux.HandleFunc("GET /deploy", s.uiDeployPage)
	mux.HandleFunc("POST /deploy", s.uiDeploySubmit)
	mux.HandleFunc("GET /deployments/{name}", s.uiDeploymentDetail)
	mux.HandleFunc("DELETE /deployments/{name}", s.uiStopDeployment)
	mux.HandleFunc("GET /models", s.uiModels)
	mux.HandleFunc("POST /models/refresh", s.uiRefreshModels)
	mux.HandleFunc("GET /downloads", s.uiDownloads)
	mux.HandleFunc("POST /downloads", s.uiStartDownload)
	mux.HandleFunc("DELETE /downloads/{id...}", s.uiRemoveDownload)
	mux.HandleFunc("POST /hardware/refresh", s.uiRescanGPUs)
	mux.HandleFunc("GET /benchmarks", s.uiBenchmarks)
	mux.HandleFunc("POST /benchmarks/suites", s.uiCreateSuite)
	mux.HandleFunc("GET /benchmarks/suites/{id}", s.uiSuitePage)
	mux.HandleFunc("DELETE /benchmarks/suites/{id}", s.uiDeleteSuite)
	mux.HandleFunc("POST /benchmarks/suites/{id}/configs", s.uiAddConfig)
	mux.HandleFunc("DELETE /benchmarks/suites/{id}/configs/{idx}", s.uiRemoveConfig)
	mux.HandleFunc("POST /benchmarks/datasets", s.uiUploadDataset)
	mux.HandleFunc("DELETE /benchmarks/datasets/{id}", s.uiDeleteDataset)
	mux.HandleFunc("POST /benchmarks/runs", s.uiStartRun)
	mux.HandleFunc("GET /benchmarks/runs/{id}", s.uiRunPage)
	mux.HandleFunc("POST /benchmarks/runs/{id}/cancel", s.uiCancelRun)
	mux.HandleFunc("DELETE /benchmarks/runs/{id}", s.uiDeleteRun)

	// htmx fragments
	mux.HandleFunc("GET /partials/bench-run", s.partialRun)
	mux.HandleFunc("GET /partials/bench-runs", s.partialRuns)
	mux.HandleFunc("GET /partials/health", s.partialHealth)
	mux.HandleFunc("GET /partials/deployments", s.partialDeployments)
	mux.HandleFunc("GET /partials/node-stats", s.partialNodeStats)
	mux.HandleFunc("GET /partials/logs", s.partialLogs)
	mux.HandleFunc("GET /partials/models", s.partialModels)
	mux.HandleFunc("GET /partials/model-select", s.partialModelSelect)
	mux.HandleFunc("GET /partials/downloads", s.partialDownloads)
	mux.HandleFunc("GET /partials/download-logs", s.partialDownloadLogs)
	mux.HandleFunc("GET /partials/gpus", s.partialGPUs)

	// static assets
	mux.Handle("GET /static/", http.FileServerFS(web.FS))

	var h http.Handler = mux
	if s.cfg.BasicAuthEnabled() {
		h = s.basicAuth(h)
	}
	return s.logRequests(h)
}

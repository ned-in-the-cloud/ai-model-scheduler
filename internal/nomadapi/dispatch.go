package nomadapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"
)

// Dispatch launches an instance of a parameterized batch job and returns the
// dispatched job's ID.
func (c *Client) Dispatch(jobID string, meta map[string]string) (string, error) {
	resp, _, err := c.c.Jobs().Dispatch(jobID, meta, nil, "", nil)
	if err != nil {
		return "", fmt.Errorf("dispatching %s: %w", jobID, err)
	}
	return resp.DispatchedJobID, nil
}

// BatchResult is the terminal state of a dispatched batch job.
type BatchResult struct {
	AllocID      string
	ClientStatus string // complete | failed
}

// BatchFailureReason returns the most useful explanation for a failed batch
// allocation: the stderr tail if the task produced any, otherwise the most
// telling task lifecycle event.
func (c *Client) BatchFailureReason(allocID string) string {
	stderr, _ := c.TailLogs(allocID, TaskName, "stderr", 4*1024)
	if reason := strings.TrimSpace(stderr); reason != "" {
		return reason
	}
	events, err := c.AllocEvents(allocID)
	if err != nil || len(events) == 0 {
		return "no output"
	}
	for _, ev := range events {
		if ev.Type == "Driver Failure" || ev.Type == "Failed Validation" {
			return ev.Message
		}
	}
	return events[len(events)-1].Message
}

// WaitForBatch polls a dispatched batch job until its allocation reaches a
// terminal state or the context expires.
func (c *Client) WaitForBatch(ctx context.Context, dispatchedJobID string) (BatchResult, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		alloc, err := c.LatestAlloc(dispatchedJobID)
		if err == nil && alloc != nil {
			switch alloc.ClientStatus {
			case api.AllocClientStatusComplete, api.AllocClientStatusFailed:
				return BatchResult{AllocID: alloc.ID, ClientStatus: alloc.ClientStatus}, nil
			}
		}
		select {
		case <-ctx.Done():
			return BatchResult{}, fmt.Errorf("waiting for %s: %w", dispatchedJobID, ctx.Err())
		case <-ticker.C:
		}
	}
}

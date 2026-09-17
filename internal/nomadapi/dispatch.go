package nomadapi

import (
	"context"
	"fmt"
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

package nomadapi

import (
	"fmt"
	"strings"

	"github.com/hashicorp/nomad/api"
)

// TaskName is the task name used in every job this application creates.
const TaskName = "server"

// TailLogs returns up to maxBytes from the end of a task's log stream
// ("stdout" or "stderr") for the given allocation.
func (c *Client) TailLogs(allocID, task, stream string, maxBytes int64) (string, error) {
	return c.readLogs(allocID, task, stream, api.OriginEnd, maxBytes)
}

// ReadAllLogs returns a task's full log stream from the beginning. Intended
// for short-lived helper jobs whose stdout is a machine-readable result.
func (c *Client) ReadAllLogs(allocID, task, stream string) (string, error) {
	return c.readLogs(allocID, task, stream, api.OriginStart, 0)
}

func (c *Client) readLogs(allocID, task, stream, origin string, offset int64) (string, error) {
	alloc, _, err := c.c.Allocations().Info(allocID, nil)
	if err != nil {
		return "", fmt.Errorf("fetching allocation %s: %w", allocID, err)
	}
	cancel := make(chan struct{})
	defer close(cancel)
	frames, errCh := c.c.AllocFS().Logs(alloc, false, task, stream, origin, offset, cancel, nil)

	var sb strings.Builder
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				return sb.String(), nil
			}
			if frame != nil {
				sb.Write(frame.Data)
			}
		case err := <-errCh:
			// A partial read is still useful; return what we have with the error.
			return sb.String(), err
		}
	}
}

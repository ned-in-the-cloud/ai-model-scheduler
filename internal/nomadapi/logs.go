package nomadapi

import (
	"fmt"
	"regexp"
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

// podman's k8s-file log driver prefixes every line with a timestamp, the
// stream name, and a partial/full marker:
//
//	2026-09-17T17:57:25.585794651+00:00 stdout F downloading ...
//
// The docker driver returns raw output. Normalize so callers always see the
// program's own output: strip the prefix and join partial (P) chunks, which
// are one logical line split across entries.
var podmanLogLineRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\S+ (?:stdout|stderr) ([PF]) ?(.*)$`)

func normalizePodmanLogs(s string) string {
	if s == "" {
		return s
	}
	var sb strings.Builder
	for line := range strings.Lines(s) {
		m := podmanLogLineRe.FindStringSubmatch(strings.TrimSuffix(line, "\n"))
		if m == nil {
			sb.WriteString(line)
			continue
		}
		sb.WriteString(m[2])
		if m[1] == "F" {
			sb.WriteString("\n")
		}
	}
	return sb.String()
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
				return normalizePodmanLogs(sb.String()), nil
			}
			if frame != nil {
				sb.Write(frame.Data)
			}
		case err := <-errCh:
			// A partial read is still useful; return what we have with the error.
			return normalizePodmanLogs(sb.String()), err
		}
	}
}

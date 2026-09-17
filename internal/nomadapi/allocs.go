package nomadapi

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/hashicorp/nomad/api"
)

// LatestAlloc returns the most recently created allocation for a job, or nil
// if the job has none.
func (c *Client) LatestAlloc(jobID string) (*api.Allocation, error) {
	stubs, _, err := c.c.Jobs().Allocations(jobID, false, nil)
	if err != nil {
		return nil, fmt.Errorf("listing allocations for %s: %w", jobID, err)
	}
	var latest *api.AllocationListStub
	for _, s := range stubs {
		if latest == nil || s.CreateIndex > latest.CreateIndex {
			latest = s
		}
	}
	if latest == nil {
		return nil, nil
	}
	alloc, _, err := c.c.Allocations().Info(latest.ID, nil)
	if err != nil {
		return nil, fmt.Errorf("fetching allocation %s: %w", latest.ID, err)
	}
	return alloc, nil
}

// AllocEvent is one task lifecycle event, for surfacing failure reasons.
type AllocEvent struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
}

// AllocEvents returns the task's lifecycle events for an allocation, oldest
// first.
func (c *Client) AllocEvents(allocID string) ([]AllocEvent, error) {
	alloc, _, err := c.c.Allocations().Info(allocID, nil)
	if err != nil {
		return nil, fmt.Errorf("fetching allocation %s: %w", allocID, err)
	}
	state, ok := alloc.TaskStates[TaskName]
	if !ok {
		return nil, nil
	}
	events := make([]AllocEvent, 0, len(state.Events))
	for _, ev := range state.Events {
		msg := ev.DisplayMessage
		if msg == "" {
			msg = ev.Message
		}
		events = append(events, AllocEvent{
			Time:    time.Unix(0, ev.Time),
			Type:    ev.Type,
			Message: msg,
		})
	}
	return events, nil
}

// allocEndpoint extracts the host endpoint (ip:port) for the PortLabel port
// from an allocation's actual port mapping.
func allocEndpoint(alloc *api.Allocation) (string, int) {
	if alloc.AllocatedResources != nil {
		for _, p := range alloc.AllocatedResources.Shared.Ports {
			if p.Label == PortLabel {
				return net.JoinHostPort(p.HostIP, strconv.Itoa(p.Value)), p.Value
			}
		}
		for _, nw := range alloc.AllocatedResources.Shared.Networks {
			for _, p := range append(nw.ReservedPorts, nw.DynamicPorts...) {
				if p.Label == PortLabel {
					return net.JoinHostPort(nw.IP, strconv.Itoa(p.Value)), p.Value
				}
			}
		}
	}
	// Fallback for older allocation payloads.
	if alloc.Resources != nil {
		for _, nw := range alloc.Resources.Networks {
			for _, p := range append(nw.ReservedPorts, nw.DynamicPorts...) {
				if p.Label == PortLabel {
					return net.JoinHostPort(nw.IP, strconv.Itoa(p.Value)), p.Value
				}
			}
		}
	}
	return "", 0
}

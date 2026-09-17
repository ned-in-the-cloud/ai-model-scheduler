package nomadapi

import (
	"fmt"
	"time"
)

// NodeStats is a snapshot of one client node's utilization.
type NodeStats struct {
	NodeID     string        `json:"node_id"`
	NodeName   string        `json:"node_name"`
	Status     string        `json:"status"`
	CPUPercent float64       `json:"cpu_percent"`
	MemUsed    int64         `json:"mem_used_bytes"`
	MemTotal   int64         `json:"mem_total_bytes"`
	MemPercent float64       `json:"mem_percent"`
	Uptime     time.Duration `json:"uptime_seconds"`
}

// NodeStats returns utilization for every ready client node (typically one).
func (c *Client) NodeStats() ([]NodeStats, error) {
	nodes, _, err := c.c.Nodes().List(nil)
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	var out []NodeStats
	for _, n := range nodes {
		ns := NodeStats{NodeID: n.ID, NodeName: n.Name, Status: n.Status}
		if n.Status == "ready" {
			if hs, err := c.c.Nodes().Stats(n.ID, nil); err == nil {
				var busy float64
				for _, core := range hs.CPU {
					busy += 100 - core.Idle
				}
				if len(hs.CPU) > 0 {
					ns.CPUPercent = busy / float64(len(hs.CPU))
				}
				if hs.Memory != nil {
					ns.MemUsed = int64(hs.Memory.Used)
					ns.MemTotal = int64(hs.Memory.Total)
					if ns.MemTotal > 0 {
						ns.MemPercent = float64(ns.MemUsed) / float64(ns.MemTotal) * 100
					}
				}
				ns.Uptime = time.Duration(hs.Uptime) * time.Second
			}
		}
		out = append(out, ns)
	}
	return out, nil
}

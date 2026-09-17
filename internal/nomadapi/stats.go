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
	GPUs       []string      `json:"gpus,omitempty"` // e.g. "nvidia/gpu/RTX 4090 ×1"
}

// AllocUsage is a point-in-time resource usage sample for one allocation.
type AllocUsage struct {
	CPUMHz   float64 `json:"cpu_mhz"`
	MemBytes int64   `json:"mem_bytes"`
}

// AllocStats fetches current CPU/memory usage for an allocation.
func (c *Client) AllocStats(allocID string) (AllocUsage, error) {
	alloc, _, err := c.c.Allocations().Info(allocID, nil)
	if err != nil {
		return AllocUsage{}, fmt.Errorf("fetching allocation %s: %w", allocID, err)
	}
	usage, err := c.c.Allocations().Stats(alloc, nil)
	if err != nil {
		return AllocUsage{}, fmt.Errorf("fetching stats for %s: %w", allocID, err)
	}
	var out AllocUsage
	if ru := usage.ResourceUsage; ru != nil {
		if ru.CpuStats != nil {
			out.CPUMHz = ru.CpuStats.TotalTicks
		}
		if ru.MemoryStats != nil {
			out.MemBytes = int64(ru.MemoryStats.RSS)
			if out.MemBytes == 0 {
				out.MemBytes = int64(ru.MemoryStats.Usage)
			}
		}
	}
	return out, nil
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
			if info, _, err := c.c.Nodes().Info(n.ID, nil); err == nil && info.NodeResources != nil {
				for _, dev := range info.NodeResources.Devices {
					if dev.Type == "gpu" {
						ns.GPUs = append(ns.GPUs, fmt.Sprintf("%s ×%d", dev.Name, len(dev.Instances)))
					}
				}
			}
		}
		out = append(out, ns)
	}
	return out, nil
}

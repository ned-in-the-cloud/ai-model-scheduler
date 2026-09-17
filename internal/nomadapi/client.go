// Package nomadapi wraps the official Nomad API client. It is the only
// package in the application that imports github.com/hashicorp/nomad/api;
// everything above it works with the small domain types defined here so
// handlers and services can be tested against fakes.
package nomadapi

import (
	"fmt"

	"github.com/hashicorp/nomad/api"
)

// Client wraps a Nomad API client.
type Client struct {
	c *api.Client
}

// New builds a client for the given Nomad address and optional ACL token.
func New(addr, token string) (*Client, error) {
	cfg := api.DefaultConfig()
	cfg.Address = addr
	if token != "" {
		cfg.SecretID = token
	}
	c, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating nomad client: %w", err)
	}
	return &Client{c: c}, nil
}

// Health describes cluster reachability.
type Health struct {
	Reachable bool   `json:"reachable"`
	Leader    string `json:"leader,omitempty"`
	Nodes     int    `json:"nodes"`
	Error     string `json:"error,omitempty"`
}

// Health checks that the Nomad cluster is reachable and has a leader.
func (c *Client) Health() Health {
	leader, err := c.c.Status().Leader()
	if err != nil {
		return Health{Reachable: false, Error: err.Error()}
	}
	h := Health{Reachable: true, Leader: leader}
	nodes, _, err := c.c.Nodes().List(nil)
	if err == nil {
		h.Nodes = len(nodes)
	}
	return h
}

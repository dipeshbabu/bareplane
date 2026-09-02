package provider

import (
	"context"

	"github.com/dipeshbabu/bareplane/internal/config"
)

// Provider identifies an infrastructure backend and validates its configuration.
type Provider interface {
	Type() string
	Validate(config.Provider) error
}

// Discoverer is implemented by providers that can inspect existing infrastructure.
type Discoverer interface {
	Discover(context.Context) (Inventory, error)
}

// Planner is implemented by providers that can compare desired state with discovered infrastructure.
type Planner interface {
	Plan(context.Context, config.Config, Inventory) (Plan, error)
}

type Inventory struct {
	Nodes []Node
}

type Node struct {
	ID       string
	Name     string
	Role     string
	CPU      int
	MemoryGB int
	DiskGB   int
	GPU      bool
}

type Plan struct {
	Changes []Change
}

type Change struct {
	Action Action
	Kind   string
	Name   string
	Reason string
}

type Action string

const (
	ActionCreate Action = "create"
	ActionUpdate Action = "update"
	ActionDelete Action = "delete"
	ActionNoop   Action = "noop"
)

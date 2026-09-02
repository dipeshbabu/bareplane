package proxmox

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/provider"
)

const bytesPerGiB int64 = 1 << 30

// RuntimeProvider is a configured Proxmox provider with a read-only API client.
// It is separate from Provider so configuration-only resolution never performs
// network operations or implies runtime capabilities.
type RuntimeProvider struct {
	provider *Provider
	client   *Client
}

func NewRuntime(cfg config.Provider, client *Client) (*RuntimeProvider, error) {
	if client == nil {
		return nil, fmt.Errorf("proxmox client is nil")
	}

	resolved, err := New(cfg)
	if err != nil {
		return nil, err
	}
	base, ok := resolved.(*Provider)
	if !ok {
		return nil, fmt.Errorf("initialize proxmox runtime: unexpected provider type %T", resolved)
	}
	return &RuntimeProvider{provider: base, client: client}, nil
}

func (p *RuntimeProvider) Type() string {
	return Type
}

func (p *RuntimeProvider) Validate(cfg config.Provider) error {
	if p == nil || p.provider == nil {
		return fmt.Errorf("proxmox runtime provider is not initialized")
	}
	return p.provider.Validate(cfg)
}

func (p *RuntimeProvider) Discover(ctx context.Context) (provider.Inventory, error) {
	if p == nil || p.provider == nil || p.client == nil {
		return provider.Inventory{}, fmt.Errorf("proxmox runtime provider is not initialized")
	}

	resources, err := p.client.VMResources(ctx)
	if err != nil {
		return provider.Inventory{}, fmt.Errorf("discover proxmox guests: %w", err)
	}

	nodes := make([]provider.Node, 0, len(resources))
	for _, resource := range resources {
		if resource.VMID <= 0 {
			return provider.Inventory{}, fmt.Errorf("discover proxmox guests: invalid VM ID %d", resource.VMID)
		}

		memoryGB, err := bytesToGiB(resource.MaxMemory)
		if err != nil {
			return provider.Inventory{}, fmt.Errorf("guest %d memory capacity: %w", resource.VMID, err)
		}
		diskGB, err := bytesToGiB(resource.MaxDisk)
		if err != nil {
			return provider.Inventory{}, fmt.Errorf("guest %d disk capacity: %w", resource.VMID, err)
		}

		kind := strings.ToLower(strings.TrimSpace(resource.Type))
		if kind == "" {
			kind = "guest"
		}
		nodes = append(nodes, provider.Node{
			ID:       fmt.Sprintf("%s/%d", kind, resource.VMID),
			Name:     strings.TrimSpace(resource.Name),
			Kind:     kind,
			Host:     strings.TrimSpace(resource.Node),
			Status:   strings.TrimSpace(resource.Status),
			CPU:      resource.MaxCPU,
			MemoryGB: memoryGB,
			DiskGB:   diskGB,
			Template: resource.Template != 0,
			Tags:     parseTags(resource.Tags),
		})
	}

	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Name == nodes[j].Name {
			return nodes[i].ID < nodes[j].ID
		}
		return nodes[i].Name < nodes[j].Name
	})
	return provider.Inventory{Nodes: nodes}, nil
}

func parseTags(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	seen := make(map[string]struct{})
	for _, part := range strings.Split(raw, ";") {
		tag := strings.TrimSpace(part)
		if tag == "" {
			continue
		}
		seen[tag] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}

	tags := make([]string, 0, len(seen))
	for tag := range seen {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

func bytesToGiB(value int64) (int, error) {
	if value < 0 {
		return 0, fmt.Errorf("value must not be negative")
	}
	if value == 0 {
		return 0, nil
	}

	gib := value / bytesPerGiB
	if value%bytesPerGiB != 0 {
		gib++
	}
	maxInt := int64(^uint(0) >> 1)
	if gib > maxInt {
		return 0, fmt.Errorf("value exceeds platform integer range")
	}
	return int(gib), nil
}

var _ provider.Provider = (*RuntimeProvider)(nil)
var _ provider.Discoverer = (*RuntimeProvider)(nil)

package gitops

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
)

const MetricsServerVersion = "0.9.0"

// Metrics Server's reviewed upstream envelope supports at most 5,000 nodes.
// Count without expanding a topology or risking overflow from malformed input.
func metricsResources(cfg config.Config) (cpu, memory, limit string, err error) {
	nodes, err := cfg.MetricsNodeCount()
	if err != nil {
		return "", "", "", err
	}
	units := max(nodes, 100)
	return fmt.Sprintf("%dm", units), fmt.Sprintf("%dMi", units*2), fmt.Sprintf("%dMi", units*4), nil
}

func renderMetricsResources(cfg config.Config, files map[string][]byte) error {
	cpu, memory, limit, err := metricsResources(cfg)
	if err != nil {
		return err
	}
	name := "components/metrics-server/upstream.yaml"
	data, present := files[name]
	if !present {
		return errors.New("Metrics Server's reviewed payload is missing")
	}
	// Replace the longer marker first: MEMORY is a prefix of MEMORY_LIMIT.
	files[name] = []byte(strings.NewReplacer("BAREPLANE_METRICS_MEMORY_LIMIT", limit,
		"BAREPLANE_METRICS_MEMORY", memory, "BAREPLANE_METRICS_CPU", cpu).Replace(string(data)))
	return nil
}

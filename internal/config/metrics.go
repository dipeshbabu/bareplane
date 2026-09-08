package config

import "errors"

const MetricsServerMaxNodes = 5000

// MetricsNodeCount checks the upstream envelope without allocating machines or
// summing untrusted counts before checking for integer overflow.
func (c Config) MetricsNodeCount() (int, error) {
	total := 0
	for _, group := range c.Spec.Nodes {
		if group.Count < 1 || group.Count > MetricsServerMaxNodes-total {
			return 0, errors.New("Metrics Server requires between 1 and 5000 desired nodes")
		}
		total += group.Count
	}
	if total == 0 {
		return 0, errors.New("Metrics Server requires at least one desired node")
	}
	return total, nil
}

func (c Config) validateMetricsSizing() []string {
	resolved, err := c.ResolvePlatform()
	if err != nil {
		return nil // The component/profile validator reports structural errors.
	}
	for _, component := range resolved.Components {
		if component.ID == "metrics-server" {
			if _, err := c.MetricsNodeCount(); err != nil {
				return []string{err.Error()}
			}
			break
		}
	}
	return nil
}

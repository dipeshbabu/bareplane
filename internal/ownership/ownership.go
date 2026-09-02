package ownership

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	ManagerTag       = "bareplane"
	ClusterTagPrefix = "bareplane-cluster-"
)

var clusterNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func ClusterTag(cluster string) (string, error) {
	if !clusterNamePattern.MatchString(cluster) {
		return "", fmt.Errorf("cluster name %q is invalid for ownership metadata", cluster)
	}
	return ClusterTagPrefix + cluster, nil
}

func Tags(cluster string) ([]string, error) {
	clusterTag, err := ClusterTag(cluster)
	if err != nil {
		return nil, err
	}
	return []string{ManagerTag, clusterTag}, nil
}

func Matches(tags []string, cluster string) bool {
	clusterTag, err := ClusterTag(cluster)
	if err != nil {
		return false
	}

	hasManager := false
	hasCluster := false
	for _, tag := range tags {
		switch strings.TrimSpace(tag) {
		case ManagerTag:
			hasManager = true
		case clusterTag:
			hasCluster = true
		}
	}
	return hasManager && hasCluster
}

func Normalize(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(tags))
	for _, raw := range tags {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			continue
		}
		seen[tag] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}

	normalized := make([]string, 0, len(seen))
	for tag := range seen {
		normalized = append(normalized, tag)
	}
	sort.Strings(normalized)
	return normalized
}

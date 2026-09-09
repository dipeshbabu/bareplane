package gitops

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
)

const PrometheusVersion = "3.14.0"
const KubeStateMetricsVersion = "2.20.0"

func renderObservability(cfg config.Config, files map[string][]byte) error {
	settings, err := cfg.RequireObservability()
	if err != nil {
		return err
	}
	const configuration = "components/observability/configuration.yaml"
	const deployment = "components/observability/prometheus.yaml"
	if len(files[configuration]) == 0 || len(files[deployment]) == 0 {
		return fmt.Errorf("reviewed observability payload is missing")
	}
	// Only validated integer hours enter the nested Prometheus configuration.
	files[configuration] = []byte(strings.ReplaceAll(string(files[configuration]), "BAREPLANE_OBSERVABILITY_RETENTION_HOURS", strconv.Itoa(settings.RetentionHours)))
	digest := sha256.Sum256(files[configuration])
	files[deployment] = []byte(strings.ReplaceAll(string(files[deployment]), "BAREPLANE_OBSERVABILITY_CONFIG_SHA256", fmt.Sprintf("%x", digest)))
	return nil
}

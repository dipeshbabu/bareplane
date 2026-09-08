package gitops

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

const ExternalDNSVersion = "0.22.0"

func renderDNS(cfg config.Config, files map[string][]byte) error {
	settings, err := cfg.RequireDNSAutomation()
	if err != nil {
		return err
	}
	data, err := assets.ReadFile("assets/external-dns/upstream.yaml")
	if err != nil {
		return err
	}
	replace := strings.NewReplacer(
		"BAREPLANE_CLUSTER_NAME", cfg.Metadata.Name,
		"BAREPLANE_DNS_SOURCE_NAMESPACE", settings.SourceNamespace,
		"BAREPLANE_DNS_CREDENTIAL_REVISION", settings.TokenSecret.EffectiveRevision(),
		"BAREPLANE_DNS_ZONE_ID", settings.ZoneID,
		"BAREPLANE_DNS_DOMAIN", settings.Domain,
		"BAREPLANE_DNS_OWNER_ID", settings.OwnerID,
		"BAREPLANE_DNS_SECRET_NAME", settings.TokenSecret.Name,
		"BAREPLANE_DNS_SECRET_KEY", settings.TokenSecret.Key,
	)
	var visit func(*yaml.Node)
	visit = func(node *yaml.Node) {
		if node.Kind == yaml.ScalarNode && node.Tag == "!!str" && strings.Contains(node.Value, "BAREPLANE_") {
			node.Value = replace.Replace(node.Value)
			// Keep standalone fields and embedded arguments as strings in YAML
			// 1.1 and 1.2, including numeric Secret names/keys and revisions.
			node.Style = yaml.DoubleQuotedStyle
		}
		children := node.Content[:0]
		for _, child := range node.Content {
			// Kingpin boolean switches do not accept --flag=true/false. Apply
			// mode deliberately omits this switch; dry-run retains its presence.
			if node.Kind == yaml.SequenceNode && child.Kind == yaml.ScalarNode && child.Value == "--dry-run" && settings.EffectiveMode() == "apply" {
				continue
			}
			visit(child)
			children = append(children, child)
		}
		node.Content = children
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var document yaml.Node
		if err := decoder.Decode(&document); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return err
		}
		visit(&document)
		if err := encoder.Encode(&document); err != nil {
			return err
		}
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	if bytes.Contains(output.Bytes(), []byte("BAREPLANE_")) {
		return errors.New("ExternalDNS payload contains an unresolved public input")
	}
	files["components/external-dns/upstream.yaml"] = output.Bytes()
	return nil
}

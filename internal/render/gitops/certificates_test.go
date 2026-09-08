package gitops

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

func TestCertManagerPayloadIsPinnedOptInAndNamespaceScoped(t *testing.T) {
	cfg := fixture(t)
	base, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := base["components/cert-manager/upstream.yaml"]; exists {
		t.Fatal("cert-manager enabled implicitly")
	}
	cfg.Spec.Components = &config.ComponentSelection{Enabled: []string{"cert-manager"}}
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	data, err := assets.ReadFile("assets/cert-manager/upstream.yaml")
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != "e32fecbed6ca179826fa86aa307dbc8ffb272b2ab490d7f845274dc8be356df5" {
		t.Fatalf("unreviewed cert-manager asset: %s", got)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(files["components/cert-manager/upstream.yaml"]))
	deployments, crds := map[string]bool{}, 0
	for {
		var obj map[string]any
		err := decoder.Decode(&obj)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		meta := obj["metadata"].(map[string]any)
		if meta["namespace"] == "kube-system" {
			t.Fatal("platform component claimed bootstrap namespace")
		}
		if meta["annotations"].(map[string]any)["bareplane.io/cluster"] != cfg.Metadata.Name {
			t.Fatal("component ownership missing")
		}
		if obj["kind"] == "Secret" {
			t.Fatal("upstream payload unexpectedly contains credentials")
		}
		if obj["kind"] == "CustomResourceDefinition" {
			crds++
		}
		if obj["kind"] == "Deployment" {
			deployments[meta["name"].(string)] = true
			pod := obj["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			if len(pod["tolerations"].([]any)) != 1 {
				t.Fatal("single-control-plane scheduling unsupported")
			}
			for _, item := range pod["containers"].([]any) {
				container := item.(map[string]any)
				name := container["name"].(string)
				if container["image"] != "quay.io/jetstack/"+name+":v"+CertManagerVersion {
					t.Fatalf("unpinned image: %v", container["image"])
				}
				if container["resources"] == nil {
					t.Fatal("unbounded component requests")
				}
				for _, arg := range container["args"].([]any) {
					if arg == "--leader-election-namespace=kube-system" {
						t.Fatal("leader election overlaps bootstrap namespace")
					}
				}
			}
		}
	}
	if crds != 6 || !reflect.DeepEqual(deployments, map[string]bool{"cert-manager": true, "cert-manager-cainjector": true, "cert-manager-webhook": true}) {
		t.Fatalf("wrong component shape: %d %v", crds, deployments)
	}
	var app map[string]any
	if err := yaml.Unmarshal(files[cfg.Spec.GitOps.RootPath+"/applications/cert-manager.yaml"], &app); err != nil {
		t.Fatal(err)
	}
	if app["spec"].(map[string]any)["destination"].(map[string]any)["namespace"] != "cert-manager" {
		t.Fatal("wrong Application namespace")
	}
}

func TestCertificateIssuersRenderDeterministicallyWithoutPrivateMaterial(t *testing.T) {
	cfg := fixture(t)
	cfg.Spec.Certificates = &config.CertificateConfig{Issuers: []config.CertificateIssuer{{Name: "z-ca", Type: "ca", SecretName: "operator-ca"}, {Name: "a-lab", Type: "self-signed"}}}
	first, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spec.Certificates.Issuers[0], cfg.Spec.Certificates.Issuers[1] = cfg.Spec.Certificates.Issuers[1], cfg.Spec.Certificates.Issuers[0]
	second, err := Render(cfg)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("issuer ordering changed output")
	}
	data := first["components/cert-manager/issuers.yaml"]
	if bytes.Contains(data, []byte("kind: Secret")) || bytes.Contains(data, []byte("privateKey")) || !bytes.Contains(data, []byte("secretName: operator-ca")) {
		t.Fatal("issuer leaked or omitted secret-reference contract")
	}
	if !bytes.Contains(first["components/cert-manager/kustomization.yaml"], []byte("issuers.yaml")) {
		t.Fatal("issuer payload is not reconciled")
	}
}

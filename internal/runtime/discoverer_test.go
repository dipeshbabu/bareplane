package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/provider/proxmox"
)

func TestDiscovererBuildsReadOnlyProxmoxRuntime(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/cluster/resources" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"vmid":101,"type":"qemu","name":"lab-control-1","node":"pve1","status":"running","maxcpu":4,"maxmem":8589934592,"maxdisk":68719476736}]}`)
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Spec.Provider.Endpoint = server.URL
	discoverer, err := Discoverer(cfg, ProviderDependencies{
		LookupEnv:  testLookupEnv("root@pam!bareplane", "token-secret"),
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("Discoverer() error = %v", err)
	}

	inventory, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(inventory.Nodes) != 1 || inventory.Nodes[0].ID != "qemu/101" {
		t.Fatalf("unexpected inventory: %#v", inventory)
	}
}

func TestDiscovererReportsMissingProxmoxCredentials(t *testing.T) {
	cfg := config.Default()
	_, err := Discoverer(cfg, ProviderDependencies{
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if err == nil || !strings.Contains(err.Error(), proxmox.EnvTokenID) {
		t.Fatalf("expected missing credential error, got %v", err)
	}
}

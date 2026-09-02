package proxmox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVMResourcesUsesClusterVMFilter(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api2/json/cluster/resources" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("type"); got != "vm" {
			t.Fatalf("type query = %q, want vm", got)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"vmid":101,"type":"qemu","name":"worker","node":"pve1","status":"running","cpu":0.5,"maxcpu":8,"mem":1073741824,"maxmem":8589934592,"disk":2147483648,"maxdisk":68719476736,"template":0,"tags":"bareplane;gpu"}]}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, testCredentials(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	resources, err := client.VMResources(context.Background())
	if err != nil {
		t.Fatalf("VMResources() error = %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected one resource, got %d", len(resources))
	}
	resource := resources[0]
	if resource.VMID != 101 || resource.Type != "qemu" || resource.Name != "worker" || resource.MaxCPU != 8 {
		t.Fatalf("unexpected VM resource: %#v", resource)
	}
	if resource.MaxMemory != 8589934592 || resource.MaxDisk != 68719476736 || resource.Tags != "bareplane;gpu" {
		t.Fatalf("unexpected VM capacity or tags: %#v", resource)
	}
}

package proxmox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
)

func TestRuntimeProviderDiscoverTranslatesAndSortsGuests(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[
			{"vmid":202,"type":"lxc","name":"zeta","node":"pve2","status":"stopped","maxcpu":2,"maxmem":3221225472,"maxdisk":10737418241,"template":1,"tags":" beta ;alpha;beta;; "},
			{"vmid":101,"type":"qemu","name":"alpha","node":"pve1","status":"running","maxcpu":8,"maxmem":8589934592,"maxdisk":68719476736,"template":0,"tags":"gpu;bareplane"},
			{"vmid":102,"type":"qemu","name":"alpha","node":"pve3","status":"stopped","maxcpu":4,"maxmem":4294967296,"maxdisk":34359738368,"template":0,"tags":""}
		]}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, testCredentials(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	runtimeProvider, err := NewRuntime(config.Provider{Type: Type, Endpoint: server.URL}, client)
	if err != nil {
		t.Fatal(err)
	}

	inventory, err := runtimeProvider.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(inventory.Nodes) != 3 {
		t.Fatalf("expected 3 inventory nodes, got %d", len(inventory.Nodes))
	}

	first := inventory.Nodes[0]
	if first.ID != "qemu/101" || first.Name != "alpha" || first.Kind != "qemu" || first.Host != "pve1" {
		t.Fatalf("unexpected first node identity: %#v", first)
	}
	if first.Status != "running" || first.CPU != 8 || first.MemoryGB != 8 || first.DiskGB != 64 || first.Template {
		t.Fatalf("unexpected first node capacity: %#v", first)
	}
	if !reflect.DeepEqual(first.Tags, []string{"bareplane", "gpu"}) {
		t.Fatalf("unexpected first node tags: %#v", first.Tags)
	}

	second := inventory.Nodes[1]
	if second.ID != "qemu/102" {
		t.Fatalf("duplicate-name tie break was not provider ID: %#v", second)
	}

	third := inventory.Nodes[2]
	if third.ID != "lxc/202" || !third.Template || third.MemoryGB != 3 || third.DiskGB != 11 {
		t.Fatalf("unexpected third node: %#v", third)
	}
	if !reflect.DeepEqual(third.Tags, []string{"alpha", "beta"}) {
		t.Fatalf("unexpected sorted/deduplicated tags: %#v", third.Tags)
	}
}

func TestRuntimeProviderDoesNotInferOwnershipFromName(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[{"vmid":101,"type":"qemu","name":"bareplane-control-plane-1","node":"pve1","status":"running","maxcpu":4,"maxmem":8589934592,"maxdisk":68719476736}]}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, testCredentials(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	runtimeProvider, err := NewRuntime(config.Provider{Type: Type, Endpoint: server.URL}, client)
	if err != nil {
		t.Fatal(err)
	}

	inventory, err := runtimeProvider.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := inventory.Nodes[0].Tags; len(got) != 0 {
		t.Fatalf("expected no ownership metadata to be invented, got tags %#v", got)
	}
}

func TestNewRuntimeRejectsClientForDifferentEndpoint(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	client, err := NewClient(server.URL, testCredentials(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRuntime(config.Provider{Type: Type, Endpoint: "https://other.example.com:8006"}, client)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected endpoint mismatch error, got %v", err)
	}
}

func TestParseTagsIsDeterministic(t *testing.T) {
	got := parseTags(" z ;a;z;; b ")
	want := []string{"a", "b", "z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseTags() = %#v, want %#v", got, want)
	}
}

func TestBytesToGiBRoundsUp(t *testing.T) {
	for _, tc := range []struct {
		bytes int64
		want  int
	}{
		{0, 0},
		{1, 1},
		{1 << 30, 1},
		{(1 << 30) + 1, 2},
	} {
		got, err := bytesToGiB(tc.bytes)
		if err != nil {
			t.Fatalf("bytesToGiB(%d) error = %v", tc.bytes, err)
		}
		if got != tc.want {
			t.Fatalf("bytesToGiB(%d) = %d, want %d", tc.bytes, got, tc.want)
		}
	}
}

func TestBytesToGiBRejectsNegativeValues(t *testing.T) {
	if _, err := bytesToGiB(-1); err == nil {
		t.Fatal("expected negative byte count error")
	}
}

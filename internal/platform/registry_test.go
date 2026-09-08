package platform

import (
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func component(id string, dependencies ...string) Component {
	return Component{ID: id, Dependencies: dependencies, Owner: GitOps, Status: Implemented, BaseWave: -30}
}

func ids(result Resolution) []string {
	var names []string
	for _, component := range result.Components {
		names = append(names, component.ID)
	}
	sort.Strings(names)
	return names
}

func TestMinimalHasOneGitOpsOwnerAndExcludesBootstrapCilium(t *testing.T) {
	registry, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, profiles := range [][]string{nil, {"minimal"}} {
		result, err := registry.Resolve(Selection{Profiles: profiles})
		if err != nil || result.RequireAvailable(false) != nil {
			t.Fatalf("minimal unavailable: %v", err)
		}
		if !reflect.DeepEqual(ids(result), []string{"argocd", "cilium"}) {
			t.Fatalf("unexpected minimal closure: %v", ids(result))
		}
		gitops := result.GitOpsComponents()
		if len(gitops) != 1 || gitops[0].ID != "argocd" || gitops[0].Wave != -30 {
			t.Fatalf("wrong emitted graph: %+v", gitops)
		}
		if result.Components[0].ID != "cilium" || result.Components[0].Owner != Bootstrap {
			t.Fatal("Cilium ownership changed")
		}
	}
}

func TestAIDataAndFullResolveCoherentButExplicitlyUnavailableGraphs(t *testing.T) {
	registry, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	union := map[string]bool{}
	for profile, required := range map[string][]string{
		"ai":   {"gpu-passthrough", "gpu-drivers", "node-feature-discovery", "gpu-scheduling", "model-cache", "model-serving", "ray", "storage"},
		"data": {"storage", "data-recovery", "postgres", "kafka", "spark", "airflow", "trino"},
	} {
		result, err := registry.Resolve(Selection{Profiles: []string{profile}})
		if err != nil {
			t.Fatal(err)
		}
		selected := map[string]Resolved{}
		for _, component := range result.Components {
			selected[component.ID] = component
			union[component.ID] = true
		}
		for _, id := range required {
			if _, ok := selected[id]; !ok {
				t.Fatalf("%s missing %s", profile, id)
			}
		}
		if err := result.RequireAvailable(false); err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("partial %s accepted: %v", profile, err)
		}
		for _, item := range result.Components {
			for _, dependency := range item.Dependencies {
				if selected[dependency].Wave >= item.Wave {
					t.Fatalf("dependency wave not earlier: %s -> %s", dependency, item.ID)
				}
			}
		}
	}
	full, err := registry.Resolve(Selection{Profiles: []string{"full"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{}
	for id := range union {
		want = append(want, id)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(ids(full), want) {
		t.Fatalf("full is not the profile union: %v", ids(full))
	}
	for _, optional := range []string{"jupyter", "open-webui", "flink", "clickhouse", "redis", "superset"} {
		if union[optional] {
			t.Fatalf("optional capability silently enabled: %s", optional)
		}
	}
}

func TestCyclesUnknownReferencesInvalidMetadataAndOwnershipInversion(t *testing.T) {
	for _, definitions := range [][]Component{
		nil,
		{component("a", "missing")},
		{component("a", "b"), component("b", "a")},
		{component("a", "a")},
		{component("a", "b", "b"), component("b")},
		{component("a"), component("a")},
		{{ID: "../escape", Owner: GitOps, Status: Implemented}},
		{{ID: "trailing-", Owner: GitOps, Status: Implemented}},
		{{ID: "a", Owner: "unknown", Status: Implemented}},
		{{ID: "a", Owner: GitOps, Status: "unknown"}},
		{{ID: "a", Owner: GitOps, Status: Implemented, Namespace: "../unsafe"}},
		{{ID: "a", Owner: Bootstrap, Status: Implemented, Namespace: "kube-system"}},
		{{ID: "a", Owner: GitOps, Status: Implemented, Profiles: []string{"full"}}},
		{{ID: "host", Owner: Bootstrap, Status: Implemented, Dependencies: []string{"a"}}, component("a")},
	} {
		if _, err := New(definitions); err == nil {
			t.Fatalf("invalid registry accepted: %+v", definitions)
		}
	}
}

func TestConflictsUnknownSelectionsAndDisabledDependencies(t *testing.T) {
	registry, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range []Selection{
		{Profiles: []string{"unknown"}},
		{Enabled: []string{"missing"}},
		{Disabled: []string{"missing"}},
		{Disabled: []string{"cilium"}},
		{Enabled: []string{"vault", "secrets-sops"}},
		{Profiles: []string{"ai"}, Disabled: []string{"gpu-drivers"}},
		{Enabled: []string{"ray"}, Disabled: []string{"ray"}},
	} {
		if _, err := registry.Resolve(selection); err == nil {
			t.Fatalf("conflicting selection accepted: %+v", selection)
		}
	}
	if _, err := new(Registry).Resolve(Selection{}); err == nil {
		t.Fatal("zero registry accepted")
	}
	if err := (Resolution{}).RequireAvailable(false); err == nil {
		t.Fatal("empty execution plan accepted")
	}
}

func TestOrderingWavesAndCopiesAreDeterministic(t *testing.T) {
	definitions := []Component{
		{ID: "foundation", Owner: GitOps, Status: Implemented, BaseWave: -30},
		{ID: "operator", Owner: GitOps, Status: Implemented, Dependencies: []string{"foundation"}, BaseWave: -20},
		{ID: "service", Owner: GitOps, Status: Implemented, Dependencies: []string{"operator"}, BaseWave: -10},
		{ID: "workload", Owner: GitOps, Status: Implemented, Dependencies: []string{"service"}, BaseWave: 0},
		{ID: "second-operator", Owner: GitOps, Status: Implemented, Dependencies: []string{"operator"}, BaseWave: -20},
	}
	registry, err := New(definitions)
	if err != nil {
		t.Fatal(err)
	}
	selection := Selection{Enabled: []string{"workload", "second-operator"}}
	want, err := registry.Resolve(selection)
	if err != nil {
		t.Fatal(err)
	}
	if got := []int{want.Components[0].Wave, want.Components[1].Wave, want.Components[2].Wave, want.Components[3].Wave, want.Components[4].Wave}; !reflect.DeepEqual(got, []int{-30, -20, -19, -10, 0}) {
		t.Fatalf("wrong waves: %v", got)
	}
	random := rand.New(rand.NewSource(72))
	for i := 0; i < 30; i++ {
		shuffled := append([]Component(nil), definitions...)
		random.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		registry, err := New(shuffled)
		if err != nil {
			t.Fatal(err)
		}
		got, err := registry.Resolve(selection)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("nondeterministic graph")
		}
	}
	definitions[1].Dependencies[0] = "changed"
	mutated, _ := registry.Resolve(selection)
	mutated.Components[1].Dependencies[0] = "changed"
	got, err := registry.Resolve(selection)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("caller mutated registry")
	}
}

func TestExperimentalAvailabilityRequiresExplicitOptIn(t *testing.T) {
	experimental := component("experiment")
	experimental.Status = Experimental
	registry, err := New([]Component{experimental})
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Resolve(Selection{Enabled: []string{"experiment"}})
	if err != nil || result.RequireAvailable(false) == nil || result.RequireAvailable(true) != nil {
		t.Fatal("experimental gate is ineffective")
	}
}

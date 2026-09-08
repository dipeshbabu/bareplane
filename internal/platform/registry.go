// Package platform resolves component ownership and dependencies independently
// of rendering. A resolved graph may describe unavailable future capabilities;
// callers must pass RequireAvailable before emitting or executing that plan.
package platform

import (
	"container/heap"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type Owner string
type Status string

const (
	Infrastructure Owner  = "infrastructure"
	Bootstrap      Owner  = "bootstrap"
	GitOps         Owner  = "gitops"
	Implemented    Status = "implemented"
	Experimental   Status = "experimental"
	Unavailable    Status = "unavailable"
)

type Component struct {
	ID             string
	Dependencies   []string
	Conflicts      []string
	Owner          Owner
	Status         Status
	DefaultEnabled bool
	Profiles       []string
	BaseWave       int
}

type Registry struct{ components map[string]Component }
type Selection struct{ Profiles, Enabled, Disabled []string }
type Resolved struct {
	Component
	Wave int
}
type Resolution struct{ Components []Resolved }

var identifier = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func ValidIdentifier(value string) bool { return identifier.MatchString(value) }

func copyComponent(component Component) Component {
	component.Dependencies = append([]string(nil), component.Dependencies...)
	component.Conflicts = append([]string(nil), component.Conflicts...)
	component.Profiles = append([]string(nil), component.Profiles...)
	return component
}

func New(components []Component) (*Registry, error) {
	if len(components) == 0 || len(components) > 512 {
		return nil, errors.New("component registry must contain 1 to 512 components")
	}
	registry := &Registry{components: make(map[string]Component, len(components))}
	for _, component := range components {
		if !identifier.MatchString(component.ID) || (component.Owner != Infrastructure && component.Owner != Bootstrap && component.Owner != GitOps) || (component.Status != Implemented && component.Status != Experimental && component.Status != Unavailable) || component.BaseWave < -1000 || component.BaseWave > 1000 {
			return nil, errors.New("invalid component identity, ownership, status, or wave")
		}
		if _, exists := registry.components[component.ID]; exists {
			return nil, fmt.Errorf("duplicate component %s", component.ID)
		}
		for _, profile := range component.Profiles {
			if profile != "minimal" && profile != "ai" && profile != "data" {
				return nil, fmt.Errorf("component %s has invalid profile membership", component.ID)
			}
		}
		component = copyComponent(component)
		sort.Strings(component.Dependencies)
		sort.Strings(component.Conflicts)
		sort.Strings(component.Profiles)
		registry.components[component.ID] = component
	}
	for _, id := range registry.ids() {
		component := registry.components[id]
		for _, refs := range [][]string{component.Dependencies, component.Conflicts} {
			seen := map[string]bool{}
			for _, ref := range refs {
				if _, ok := registry.components[ref]; !ok {
					return nil, fmt.Errorf("component %s references unknown component %s", id, ref)
				}
				if ref == id || seen[ref] {
					return nil, fmt.Errorf("component %s has a self or duplicate reference", id)
				}
				seen[ref] = true
			}
		}
	}
	// Validate every definition, including currently unselected future components.
	for _, id := range registry.ids() {
		component := registry.components[id]
		for _, dependency := range component.Dependencies {
			if ownerRank(registry.components[dependency].Owner) > ownerRank(component.Owner) {
				return nil, fmt.Errorf("component %s depends on a later ownership layer", id)
			}
		}
	}
	if _, err := registry.ordered(registry.all()); err != nil {
		return nil, err
	}
	return registry, nil
}

func ownerRank(owner Owner) int {
	switch owner {
	case Infrastructure:
		return 0
	case Bootstrap:
		return 1
	default:
		return 2
	}
}

func (r *Registry) ids() []string {
	ids := make([]string, 0, len(r.components))
	for id := range r.components {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func (r *Registry) all() map[string]bool {
	all := make(map[string]bool, len(r.components))
	for id := range r.components {
		all[id] = true
	}
	return all
}

func (r *Registry) Resolve(selection Selection) (Resolution, error) {
	if r == nil || len(r.components) == 0 {
		return Resolution{}, errors.New("component registry is unavailable")
	}
	profiles := map[string]bool{}
	if len(selection.Profiles) == 0 {
		profiles["minimal"] = true
	}
	for _, profile := range selection.Profiles {
		switch profile {
		case "minimal", "ai", "data":
			profiles[profile] = true
		case "full":
			profiles["minimal"], profiles["ai"], profiles["data"] = true, true, true
		default:
			return Resolution{}, errors.New("unsupported platform profile")
		}
	}
	disabled := map[string]bool{}
	for _, id := range selection.Disabled {
		if _, ok := r.components[id]; !ok {
			return Resolution{}, fmt.Errorf("unknown disabled component %s", id)
		}
		disabled[id] = true
	}
	selected := map[string]bool{}
	var include func(string) error
	include = func(id string) error {
		component, exists := r.components[id]
		if !exists {
			return fmt.Errorf("unknown enabled component %s", id)
		}
		if disabled[id] {
			return fmt.Errorf("required component %s is explicitly disabled", id)
		}
		if selected[id] {
			return nil
		}
		selected[id] = true
		for _, dependency := range component.Dependencies {
			if err := include(dependency); err != nil {
				return err
			}
		}
		return nil
	}
	for _, id := range r.ids() {
		component := r.components[id]
		required := false
		for _, profile := range component.Profiles {
			required = required || profiles[profile]
		}
		if required || (component.DefaultEnabled && !disabled[id]) {
			if err := include(id); err != nil {
				return Resolution{}, err
			}
		}
	}
	enabled := append([]string(nil), selection.Enabled...)
	sort.Strings(enabled)
	for _, id := range enabled {
		if err := include(id); err != nil {
			return Resolution{}, err
		}
	}
	for _, id := range r.ids() {
		if selected[id] {
			for _, conflict := range r.components[id].Conflicts {
				if selected[conflict] {
					return Resolution{}, fmt.Errorf("components %s and %s conflict", id, conflict)
				}
			}
		}
	}
	return r.ordered(selected)
}

func (r *Registry) ordered(selected map[string]bool) (Resolution, error) {
	indegree := make(map[string]int, len(selected))
	dependents := make(map[string][]string, len(selected))
	for id := range selected {
		for _, dependency := range r.components[id].Dependencies {
			if selected[dependency] {
				indegree[id]++
				dependents[dependency] = append(dependents[dependency], id)
			}
		}
	}
	ready := &idHeap{}
	heap.Init(ready)
	for id := range selected {
		if indegree[id] == 0 {
			heap.Push(ready, id)
		}
	}
	waves := make(map[string]int, len(selected))
	result := Resolution{Components: make([]Resolved, 0, len(selected))}
	for ready.Len() > 0 {
		id := heap.Pop(ready).(string)
		component := r.components[id]
		wave := component.BaseWave
		for _, dependency := range component.Dependencies {
			if selected[dependency] && waves[dependency] >= wave {
				wave = waves[dependency] + 1
			}
		}
		waves[id] = wave
		result.Components = append(result.Components, Resolved{Component: copyComponent(component), Wave: wave})
		for _, dependent := range dependents[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				heap.Push(ready, dependent)
			}
		}
	}
	if len(result.Components) != len(selected) {
		return Resolution{}, errors.New("component dependency graph contains a cycle")
	}
	sort.Slice(result.Components, func(i, j int) bool {
		if result.Components[i].Wave != result.Components[j].Wave {
			return result.Components[i].Wave < result.Components[j].Wave
		}
		return result.Components[i].ID < result.Components[j].ID
	})
	return result, nil
}

// RequireAvailable is the execution/render boundary, separate from diagnostic
// graph resolution. Bootstrap/infrastructure dependencies also need implementations.
func (r Resolution) RequireAvailable(allowExperimental bool) error {
	if len(r.Components) == 0 {
		return errors.New("no platform components were selected")
	}
	missing := []string{}
	for _, component := range r.Components {
		if component.Status != Implemented && !(component.Status == Experimental && allowExperimental) {
			missing = append(missing, component.ID+" ("+string(component.Status)+")")
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("requested platform capabilities are unavailable: %s", strings.Join(missing, ", "))
}

func (r Resolution) GitOpsComponents() []Resolved {
	var components []Resolved
	for _, component := range r.Components {
		if component.Owner == GitOps {
			component.Component = copyComponent(component.Component)
			components = append(components, component)
		}
	}
	return components
}

type idHeap []string

func (h idHeap) Len() int           { return len(h) }
func (h idHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h idHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *idHeap) Push(value any)    { *h = append(*h, value.(string)) }
func (h *idHeap) Pop() any {
	previous := *h
	value := previous[len(previous)-1]
	*h = previous[:len(previous)-1]
	return value
}

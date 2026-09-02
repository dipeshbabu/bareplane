package ownership

import (
	"reflect"
	"testing"
)

func TestTags(t *testing.T) {
	got, err := Tags("lab")
	if err != nil {
		t.Fatalf("Tags() error = %v", err)
	}
	want := []string{"bareplane", "bareplane-cluster-lab"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tags() = %#v, want %#v", got, want)
	}
}

func TestMatchesRequiresManagerAndClusterTags(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags []string
		want bool
	}{
		{name: "both", tags: []string{"bareplane", "bareplane-cluster-lab"}, want: true},
		{name: "extra tags", tags: []string{"gpu", "bareplane-cluster-lab", "bareplane"}, want: true},
		{name: "manager only", tags: []string{"bareplane"}, want: false},
		{name: "cluster only", tags: []string{"bareplane-cluster-lab"}, want: false},
		{name: "wrong cluster", tags: []string{"bareplane", "bareplane-cluster-other"}, want: false},
		{name: "similar manager", tags: []string{"bareplane-managed", "bareplane-cluster-lab"}, want: false},
		{name: "case differs", tags: []string{"Bareplane", "bareplane-cluster-lab"}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Matches(tc.tags, "lab"); got != tc.want {
				t.Fatalf("Matches(%#v, lab) = %v, want %v", tc.tags, got, tc.want)
			}
		})
	}
}

func TestMatchesAllowsWhitespaceButNotCaseChanges(t *testing.T) {
	if !Matches([]string{" bareplane ", " bareplane-cluster-lab "}, "lab") {
		t.Fatal("expected trimmed exact tags to match")
	}
}

func TestNormalizeSortsAndDeduplicates(t *testing.T) {
	got := Normalize([]string{" z ", "a", "z", "", "b"})
	want := []string{"a", "b", "z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Normalize() = %#v, want %#v", got, want)
	}
}

func TestClusterTagRejectsInvalidClusterName(t *testing.T) {
	for _, name := range []string{"", "Bad", "bad_name", "-bad", "bad-"} {
		t.Run(name, func(t *testing.T) {
			if _, err := ClusterTag(name); err == nil {
				t.Fatal("expected invalid cluster name error")
			}
		})
	}
}

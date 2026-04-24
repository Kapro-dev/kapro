package controller

import (
	"testing"

	"github.com/Masterminds/semver/v3"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func TestSelectSourceVersions_DefaultsToLatestOnly(t *testing.T) {
	versions := mustVersions(t, "1.0.0", "1.0.1", "1.0.2")

	selected := selectSourceVersions(versions, nil)

	if len(selected) != 1 {
		t.Fatalf("expected 1 selected version, got %d", len(selected))
	}
	if selected[0].Original() != "1.0.2" {
		t.Fatalf("expected latest 1.0.2, got %s", selected[0].Original())
	}
}

func TestSelectSourceVersions_LastNIsBoundedAndNewestFirst(t *testing.T) {
	versions := mustVersions(t, "1.0.0", "1.0.1", "1.0.2", "1.0.3")

	selected := selectSourceVersions(versions, &kaprov1alpha1.SourceDiscoverySpec{
		Strategy: kaprov1alpha1.SourceDiscoveryLastN,
		Limit:    3,
	})

	if got := versionStrings(selected); len(got) != 3 || got[0] != "1.0.3" || got[1] != "1.0.2" || got[2] != "1.0.1" {
		t.Fatalf("expected newest three versions, got %#v", got)
	}
}

func TestTrimDiscoveredVersionsDeduplicatesAndAppliesLimit(t *testing.T) {
	discovered := []kaprov1alpha1.DiscoveredVersion{
		{Tag: "1.0.3"},
		{Tag: "1.0.2"},
		{Tag: "1.0.3"},
		{Tag: "1.0.1"},
	}

	trimmed := trimDiscoveredVersions(discovered, &kaprov1alpha1.SourceDiscoverySpec{
		Strategy: kaprov1alpha1.SourceDiscoveryLastN,
		Limit:    2,
	})

	if len(trimmed) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(trimmed))
	}
	if trimmed[0].Tag != "1.0.3" || trimmed[1].Tag != "1.0.2" {
		t.Fatalf("expected latest unique versions, got %#v", trimmed)
	}
}

func mustVersions(t *testing.T, raw ...string) []*semver.Version {
	t.Helper()
	versions := make([]*semver.Version, len(raw))
	for i, value := range raw {
		version, err := semver.NewVersion(value)
		if err != nil {
			t.Fatalf("parse version %q: %v", value, err)
		}
		versions[i] = version
	}
	return versions
}

func versionStrings(versions []*semver.Version) []string {
	out := make([]string, len(versions))
	for i, version := range versions {
		out[i] = version.Original()
	}
	return out
}

package delivery

import (
	"fmt"
	"io/fs"
	"strings"
)

// Layer media types Kapro recognises. The Helm OCI spec mandates the
// helm.chart.content media type; for raw YAML and Kustomize we own the
// namespace under "kapro.io".
const (
	MediaTypeHelmChartContent = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
	MediaTypeKustomize        = "application/vnd.kapro.kustomize.v1.tar+gzip"
	MediaTypeRawYAML          = "application/vnd.kapro.rawyaml.v1.tar+gzip"
	// MediaTypeFluxContent is the legacy media type emitted by internal/bundle —
	// we accept it as a synonym for "raw YAML or Kustomize tarball" and fall
	// through to fs-structure heuristics.
	MediaTypeFluxContent = "application/vnd.cncf.flux.content.v1.tar+gzip"
)

// DetectFormat resolves the artifact format using, in order:
//  1. The layer media type (authoritative when present and recognised).
//  2. The artifact filesystem layout (Chart.yaml → Helm; kustomization.yaml → Kustomize).
//  3. Fallback: FormatRawYAML if any .yaml or .yml file is present at root or any
//     subdirectory.
//
// Returns an error only when the filesystem is empty or contains no
// recognisable manifest at all — explicit failure beats silently applying
// nothing.
func DetectFormat(pa *PulledArtifact) (Format, error) {
	if pa == nil || pa.FS == nil {
		return "", fmt.Errorf("nil pulled artifact")
	}

	// 1. Media-type fast path.
	switch pa.MediaType {
	case MediaTypeHelmChartContent:
		return FormatHelm, nil
	case MediaTypeKustomize:
		return FormatKustomize, nil
	case MediaTypeRawYAML:
		return FormatRawYAML, nil
	}

	// 2. Structure heuristics.
	hasChartYAML := hasFile(pa.FS, "Chart.yaml")
	hasKustomization := hasFile(pa.FS, "kustomization.yaml") ||
		hasFile(pa.FS, "kustomization.yml") ||
		hasFile(pa.FS, "Kustomization")
	switch {
	case hasChartYAML:
		return FormatHelm, nil
	case hasKustomization:
		return FormatKustomize, nil
	}

	// 3. Raw-YAML fallback if any *.yaml / *.yml under the tree.
	if hasAnyYAML(pa.FS) {
		return FormatRawYAML, nil
	}
	return "", fmt.Errorf("artifact has no Chart.yaml, no kustomization.yaml, and no *.yaml files")
}

// hasFile returns true when the named file exists in fsys (regular file or
// symlink, not directory). Path is treated as the artifact-root-relative
// path with forward slashes.
func hasFile(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// hasAnyYAML walks the filesystem looking for at least one regular .yaml /
// .yml file. Returns on first hit — does not enumerate.
func hasAnyYAML(fsys fs.FS) bool {
	found := false
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		lower := strings.ToLower(p)
		if strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

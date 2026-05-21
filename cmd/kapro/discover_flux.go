package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

var defaultFluxDiscoveryPrefixes = []string{
	"flux",
	"apps",
	"clusters",
	"environments",
	"infrastructure",
	"deploy",
	"deployments",
	"overlays",
	"bases",
	"kustomize",
	"helm",
	"charts",
	"manifests",
}

type fluxDiscoverOptions struct {
	RepoPath     string
	OutPath      string
	Name         string
	Namespace    string
	Selector     string
	PathPrefixes []string
	ScanAll      bool
	MaxFiles     int
	MaxUnits     int
	Force        bool
}

type fluxDiscoveryResult struct {
	RepoPath       string
	ScannedFiles   int
	ParsedFiles    int
	Objects        []argoDiscoveredObject
	SelectedUnits  []argoDiscoveredUnit
	SkippedObjects []argoDiscoveredObject
	Errors         []string
}

func newDiscoverFluxCmd() *cobra.Command {
	opts := fluxDiscoverOptions{MaxFiles: defaultArgoDiscoveryMaxFiles, MaxUnits: defaultArgoDiscoveryMaxUnits}
	cmd := &cobra.Command{
		Use:   "flux [repo]",
		Short: "Discover an existing Flux repo and generate Kapro mapping files",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.RepoPath = "."
			if len(args) > 0 {
				opts.RepoPath = args[0]
			}
			return runFluxDiscover(opts)
		},
	}
	cmd.Flags().StringVar(&opts.OutPath, "out", "kapro-connect", "Output directory for generated Kapro files")
	cmd.Flags().StringVar(&opts.Name, "name", "flux", "Backend and Source name")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "flux-system", "Flux namespace")
	cmd.Flags().StringVar(&opts.Selector, "selector", "kapro.io/import=true", "Label selector for imported backend objects")
	cmd.Flags().StringSliceVar(&opts.PathPrefixes, "path-prefix", nil, "Repo path prefix to scan (repeatable; default: common Flux/GitOps paths)")
	cmd.Flags().BoolVar(&opts.ScanAll, "scan-all", false, "Scan all tracked YAML/JSON files instead of GitOps path prefixes")
	cmd.Flags().IntVar(&opts.MaxFiles, "max-files", defaultArgoDiscoveryMaxFiles, "Maximum tracked YAML/JSON candidate files to parse (0 = unlimited)")
	cmd.Flags().IntVar(&opts.MaxUnits, "max-units", defaultArgoDiscoveryMaxUnits, "Maximum Source units to generate (0 = unlimited)")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	return cmd
}

func runFluxDiscover(opts fluxDiscoverOptions) error {
	if opts.Name == "" {
		return fmt.Errorf("--name is required")
	}
	if opts.Namespace == "" {
		opts.Namespace = "flux-system"
	}
	if opts.OutPath == "" {
		opts.OutPath = "kapro-connect"
	}
	matchLabels, err := parseSelector(opts.Selector)
	if err != nil {
		return err
	}
	root, err := gitWorktreeRoot(opts.RepoPath)
	if err != nil {
		return err
	}
	result, err := discoverFluxRepo(root, argoDiscoveryScanOptions{
		PathPrefixes: opts.PathPrefixes,
		ScanAll:      opts.ScanAll,
		MaxFiles:     opts.MaxFiles,
		MaxUnits:     opts.MaxUnits,
	})
	if err != nil {
		return err
	}
	result.RepoPath = opts.RepoPath
	files := map[string]string{
		filepath.Join("backends", opts.Name+"-observe.yaml"): renderFluxDiscoverBackend(opts, matchLabels),
		filepath.Join("sources", opts.Name+".yaml"):          renderFluxDiscoverSource(opts, result),
		filepath.Join("discovery", "flux-discovery.yaml"):    renderFluxDiscoveryReport(result),
		filepath.Join("discovery", "kapro-git-map.yaml"):     renderFluxGitAdoptionMap(opts, result),
		filepath.Join("README.md"):                           renderFluxDiscoverReadme(opts, result),
	}
	if err := writeScaffoldFiles(opts.OutPath, files, opts.Force); err != nil {
		return err
	}
	summary := confidenceSummary(result.SelectedUnits)
	fmt.Fprintf(os.Stderr, "Discovered %d Flux objects and %d Source units from %s (confidence: high=%d medium=%d needs-review=%d)\n",
		len(result.Objects), len(result.SelectedUnits), opts.RepoPath, summary.High, summary.Medium, summary.NeedsReview)
	return nil
}

func discoverFluxRepo(root string, opts argoDiscoveryScanOptions) (fluxDiscoveryResult, error) {
	result := fluxDiscoveryResult{RepoPath: root}
	if len(opts.PathPrefixes) == 0 && !opts.ScanAll {
		opts.PathPrefixes = defaultFluxDiscoveryPrefixes
	}
	files, err := gitTrackedDiscoveryFiles(root, opts)
	if err != nil {
		return result, err
	}
	for _, file := range files {
		result.ScannedFiles++
		parsed := parseFluxDiscoveryFile(file)
		if parsed.parsed {
			result.ParsedFiles++
		}
		result.Objects = append(result.Objects, parsed.objects...)
		result.SelectedUnits = append(result.SelectedUnits, parsed.units...)
		result.SkippedObjects = append(result.SkippedObjects, parsed.skipped...)
		result.Errors = append(result.Errors, parsed.errors...)
	}
	result.SelectedUnits = dedupeUnits(result.SelectedUnits)
	if opts.MaxUnits > 0 && len(result.SelectedUnits) > opts.MaxUnits {
		return result, fmt.Errorf("discovery found %d Source units, above --max-units=%d; narrow --path-prefix or review with a higher limit", len(result.SelectedUnits), opts.MaxUnits)
	}
	sort.Slice(result.Objects, func(i, j int) bool {
		if result.Objects[i].Kind == result.Objects[j].Kind {
			return result.Objects[i].Name < result.Objects[j].Name
		}
		return result.Objects[i].Kind < result.Objects[j].Kind
	})
	sort.Slice(result.SelectedUnits, func(i, j int) bool { return result.SelectedUnits[i].Name < result.SelectedUnits[j].Name })
	return result, nil
}

type parsedFluxFile struct {
	parsed  bool
	objects []argoDiscoveredObject
	units   []argoDiscoveredUnit
	skipped []argoDiscoveredObject
	errors  []string
}

func parseFluxDiscoveryFile(file argoDiscoveryFile) parsedFluxFile {
	var parsed parsedFluxFile
	if file.Size > maxArgoDiscoveryFileSize {
		parsed.skipped = append(parsed.skipped, argoDiscoveredObject{
			Kind: "File", Path: file.RelPath, Pattern: "large-file", Reason: "file exceeds discovery size limit",
		})
		return parsed
	}
	docs, err := readYAMLOrJSONDocuments(file.AbsPath)
	if err != nil {
		parsed.errors = append(parsed.errors, fmt.Sprintf("%s: %v", file.RelPath, err))
		return parsed
	}
	if len(docs) > 0 {
		parsed.parsed = true
	}
	for _, doc := range docs {
		units, obj, ok := fluxUnitsFromObject(doc, file.RelPath)
		if ok {
			parsed.objects = append(parsed.objects, obj)
			parsed.units = append(parsed.units, units...)
			continue
		}
		if skipped, ok := skippedFluxObject(doc, file.RelPath); ok {
			parsed.skipped = append(parsed.skipped, skipped)
			continue
		}
		if units := fluxKustomizeImageUnits(doc, file.RelPath); len(units) > 0 {
			parsed.objects = append(parsed.objects, argoObjectFromDoc("", file.RelPath, doc))
			if parsed.objects[len(parsed.objects)-1].Kind == "" {
				parsed.objects[len(parsed.objects)-1] = argoDiscoveredObject{
					Kind: "KustomizationFile", Name: filepath.Base(file.RelPath), Path: file.RelPath, Pattern: "kustomize-images",
					Reason: "Kustomize image tags can be updated in Git",
				}
			}
			parsed.units = append(parsed.units, units...)
			continue
		}
		if units := helmChartUnits(doc, file.RelPath); len(units) > 0 {
			parsed.objects = append(parsed.objects, argoDiscoveredObject{
				Kind: "HelmChart", Name: filepath.Base(filepath.Dir(file.RelPath)), Path: file.RelPath, Pattern: "helm-chart",
				Reason: "Helm chart version fields can be updated in Git",
			})
			parsed.units = append(parsed.units, units...)
		}
	}
	return parsed
}

func fluxUnitsFromObject(doc map[string]any, rel string) ([]argoDiscoveredUnit, argoDiscoveredObject, bool) {
	kind := stringAt(doc, "kind")
	apiVersion := stringAt(doc, "apiVersion")
	namespace := firstNonEmpty(stringAt(doc, "metadata", "namespace"), "flux-system")
	name := fluxUnitName(doc, stringAt(doc, "metadata", "name"))
	obj := argoObjectFromDoc("", rel, doc)
	switch {
	case apiVersion == "source.toolkit.fluxcd.io/v1" && kind == "GitRepository":
		unit, ok := fluxSourceRefUnit(doc, rel, namespace, name, "GitRepository")
		return oneFluxUnit(unit, ok, obj)
	case apiVersion == "source.toolkit.fluxcd.io/v1" && kind == "OCIRepository":
		unit, ok := fluxSourceRefUnit(doc, rel, namespace, name, "OCIRepository")
		return oneFluxUnit(unit, ok, obj)
	case apiVersion == "source.toolkit.fluxcd.io/v1" && kind == "Bucket":
		unit, ok := fluxSourceRefUnit(doc, rel, namespace, name, "Bucket")
		return oneFluxUnit(unit, ok, obj)
	case strings.HasPrefix(apiVersion, "helm.toolkit.fluxcd.io/") && kind == "HelmRelease":
		units := make([]argoDiscoveredUnit, 0, 4)
		if stringAt(doc, "spec", "chart", "spec", "version") != "" {
			units = append(units, argoDiscoveredUnit{
				Name: name, BackendKind: "GitYAMLField", Namespace: namespace, SourcePath: rel,
				VersionField: "spec.chart.spec.version", Confidence: "high",
				Reason: "writes Flux HelmRelease chart version in Git",
			})
		}
		if stringAt(doc, "spec", "values", "image", "tag") != "" {
			units = append(units, argoDiscoveredUnit{
				Name: name + "-image", BackendKind: "GitYAMLField", Namespace: namespace, SourcePath: rel,
				VersionField: "spec.values.image.tag", Confidence: "medium",
				Reason: "writes obvious Flux HelmRelease values image tag in Git",
			})
		}
		units = append(units, helmReleaseValuesImageUnits(doc, rel, namespace, name)...)
		if len(units) > 0 {
			obj.Pattern = "helmrelease"
			obj.Reason = "Flux HelmRelease chart and image version fields can be updated in Git"
			return units, obj, true
		}
	case strings.HasPrefix(apiVersion, "kustomize.toolkit.fluxcd.io/") && kind == "Kustomization":
		obj.Pattern = "flux-kustomization"
		obj.Reason = "Flux Kustomization has no canonical version field; promote its Source ref or Kustomize image file instead"
		return nil, obj, false
	}
	return nil, argoDiscoveredObject{}, false
}

func oneFluxUnit(unit argoDiscoveredUnit, ok bool, obj argoDiscoveredObject) ([]argoDiscoveredUnit, argoDiscoveredObject, bool) {
	if !ok {
		return nil, argoDiscoveredObject{}, false
	}
	obj.Pattern = strings.ToLower(obj.Kind)
	obj.Reason = "Flux source revision field can be updated in Git"
	return []argoDiscoveredUnit{unit}, obj, true
}

func skippedFluxObject(doc map[string]any, rel string) (argoDiscoveredObject, bool) {
	kind := stringAt(doc, "kind")
	apiVersion := stringAt(doc, "apiVersion")
	if apiVersion == "" || kind == "" {
		return argoDiscoveredObject{}, false
	}
	if !strings.Contains(apiVersion, "toolkit.fluxcd.io/") {
		return argoDiscoveredObject{}, false
	}
	obj := argoObjectFromDoc("", rel, doc)
	obj.Pattern = "no-version-field"
	switch kind {
	case "Kustomization":
		obj.Reason = "Flux Kustomization has no canonical version field; promote its Source ref or Kustomize image file instead"
	case "HelmRepository", "ImageRepository", "ImagePolicy", "ImageUpdateAutomation":
		obj.Reason = "Flux object is part of source/image automation plumbing; Kapro does not infer a promotion write target"
	default:
		obj.Reason = "Flux object has no built-in Kapro promotion write target"
	}
	return obj, true
}

func fluxSourceRefUnit(doc map[string]any, rel, namespace, name, kind string) (argoDiscoveredUnit, bool) {
	switch {
	case stringAt(doc, "spec", "ref", "tag") != "":
		return argoDiscoveredUnit{Name: name, BackendKind: "GitYAMLField", Namespace: namespace, SourcePath: rel, VersionField: "spec.ref.tag", Confidence: "high", Reason: "writes Flux " + kind + " tag ref in Git"}, true
	case stringAt(doc, "spec", "ref", "semver") != "":
		return argoDiscoveredUnit{Name: name, BackendKind: "GitYAMLField", Namespace: namespace, SourcePath: rel, VersionField: "spec.ref.semver", Confidence: "high", Reason: "writes Flux " + kind + " semver ref in Git"}, true
	case stringAt(doc, "spec", "ref", "digest") != "":
		return argoDiscoveredUnit{Name: name, BackendKind: "GitYAMLField", Namespace: namespace, SourcePath: rel, VersionField: "spec.ref.digest", Confidence: "high", Reason: "writes Flux " + kind + " digest ref in Git"}, true
	case stringAt(doc, "spec", "ref", "branch") != "":
		return argoDiscoveredUnit{Name: name, BackendKind: "GitYAMLField", Namespace: namespace, SourcePath: rel, VersionField: "spec.ref.branch", Confidence: "medium", Reason: "writes Flux " + kind + " branch ref in Git; confirm branch promotion is intended"}, true
	default:
		return argoDiscoveredUnit{}, false
	}
}

func fluxKustomizeImageUnits(doc map[string]any, rel string) []argoDiscoveredUnit {
	if !isKustomizeConfigFile(rel, doc) {
		return nil
	}
	var units []argoDiscoveredUnit
	seenNames := map[string]int{}
	for _, image := range sliceAt(doc, "images") {
		imageName := stringAtValue(image, "name")
		if imageName == "" || stringAtValue(image, "newTag") == "" {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(filepath.Dir(rel)), "-")
		if name == "." || name == "" {
			name = strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
		}
		unitName := name + "-image"
		seenNames[unitName]++
		if seenNames[unitName] > 1 {
			unitName = fmt.Sprintf("%s-%d", unitName, seenNames[unitName])
		}
		units = append(units, argoDiscoveredUnit{
			Name: unitName, BackendKind: "KustomizeImage", SourcePath: rel, VersionField: imageName, Confidence: "high",
			Reason: "writes Kustomize image tag in Git",
		})
	}
	return units
}

func isKustomizeConfigFile(rel string, doc map[string]any) bool {
	base := strings.ToLower(filepath.Base(rel))
	if base != "kustomization.yaml" && base != "kustomization.yml" && base != "kustomization" {
		return false
	}
	if stringAt(doc, "apiVersion") != "" {
		return false
	}
	return len(sliceAt(doc, "images")) > 0
}

func helmChartUnits(doc map[string]any, rel string) []argoDiscoveredUnit {
	if strings.ToLower(filepath.Base(rel)) != "chart.yaml" {
		return nil
	}
	name := firstNonEmpty(stringAt(doc, "name"), filepath.Base(filepath.Dir(rel)))
	var units []argoDiscoveredUnit
	if stringAt(doc, "version") != "" {
		units = append(units, argoDiscoveredUnit{
			Name: name + "-chart", BackendKind: "GitYAMLField", SourcePath: rel,
			VersionField: "version", Confidence: "medium",
			Reason: "writes Helm chart package version in Git",
		})
	}
	if stringAt(doc, "appVersion") != "" {
		units = append(units, argoDiscoveredUnit{
			Name: name + "-app", BackendKind: "GitYAMLField", SourcePath: rel,
			VersionField: "appVersion", Confidence: "medium",
			Reason: "writes Helm chart appVersion in Git",
		})
	}
	return units
}

func helmReleaseValuesImageUnits(doc map[string]any, rel, namespace, baseName string) []argoDiscoveredUnit {
	values, ok := nestedMap(doc, "spec", "values")
	if !ok {
		return nil
	}
	var units []argoDiscoveredUnit
	walkImageTagFields(values, "spec.values", func(path string) {
		if path == "spec.values.image.tag" {
			return
		}
		unitName := baseName + "-" + sanitizeUnitSuffix(path)
		units = append(units, argoDiscoveredUnit{
			Name: unitName, BackendKind: "GitYAMLField", Namespace: namespace, SourcePath: rel,
			VersionField: path, Confidence: "needs-review",
			Reason: "writes a discovered HelmRelease values image tag; review custom values schema before adopting",
		})
	})
	sort.Slice(units, func(i, j int) bool { return units[i].VersionField < units[j].VersionField })
	return units
}

func walkImageTagFields(value any, path string, add func(string)) {
	m, ok := value.(map[string]any)
	if !ok {
		return
	}
	for key, child := range m {
		childPath := path + "." + key
		if strings.EqualFold(key, "tag") && pathLooksLikeImage(path) && scalarString(child) != "" {
			add(childPath)
			continue
		}
		walkImageTagFields(child, childPath, add)
	}
}

func pathLooksLikeImage(path string) bool {
	for _, part := range strings.Split(strings.ToLower(path), ".") {
		switch part {
		case "image", "images", "container", "containers":
			return true
		}
	}
	return false
}

func scalarString(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case int, int64, float64, bool:
		return strings.TrimSpace(fmt.Sprint(v))
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func sanitizeUnitSuffix(path string) string {
	path = strings.TrimPrefix(path, "spec.values.")
	path = strings.ReplaceAll(path, ".", "-")
	path = strings.ReplaceAll(path, "_", "-")
	path = strings.ToLower(path)
	var b strings.Builder
	for _, r := range path {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-")
}

func nestedMap(doc map[string]any, path ...string) (map[string]any, bool) {
	var cur any = doc
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	m, ok := cur.(map[string]any)
	return m, ok
}

func fluxUnitName(doc map[string]any, fallback string) string {
	return firstNonEmpty(
		stringAt(doc, "metadata", "labels", "kapro.io/unit"),
		stringAt(doc, "metadata", "labels", "service"),
		stringAt(doc, "metadata", "labels", "app.kubernetes.io/name"),
		fallback,
	)
}

func renderFluxDiscoverBackend(opts fluxDiscoverOptions, labels map[string]string) string {
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha2
kind: Backend
metadata:
  name: %s
spec:
  driver: flux
  runtime: Hub
  parameters:
    namespace: %s
  discovery:
    enabled: true
    managementPolicy: Observe
    maxObjects: 1000
    selector:
      matchLabels:
%s`, opts.Name, opts.Namespace, renderYAMLMap(labels, 8))
}

func renderFluxDiscoverSource(opts fluxDiscoverOptions, result fluxDiscoveryResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, `apiVersion: kapro.io/v1alpha2
kind: Source
metadata:
  name: %s
spec:
  backendRef: %s
  units:
`, opts.Name, opts.Name)
	for _, unit := range result.SelectedUnits {
		namespace := firstNonEmpty(unit.Namespace, opts.Namespace)
		fmt.Fprintf(&b, `    - name: %s
      backendKind: %s
      namespace: %s
      sourcePath: %s
      versionField: %s
`, unit.Name, unit.BackendKind, namespace, unit.SourcePath, unit.VersionField)
	}
	if len(result.SelectedUnits) == 0 {
		b.WriteString("    - name: TODO\n      backendKind: GitYAMLField\n      namespace: flux-system\n      sourcePath: path/to/flux-resource.yaml\n      versionField: spec.ref.tag\n")
	}
	return b.String()
}

func renderFluxDiscoveryReport(result fluxDiscoveryResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "repoPath: %s\nscannedFiles: %d\nparsedFiles: %d\n", result.RepoPath, result.ScannedFiles, result.ParsedFiles)
	fmt.Fprintf(&b, "objects: %d\npromotionUnits: %d\n", len(result.Objects), len(result.SelectedUnits))
	summary := confidenceSummary(result.SelectedUnits)
	fmt.Fprintf(&b, "confidence:\n  high: %d\n  medium: %d\n  needsReview: %d\n", summary.High, summary.Medium, summary.NeedsReview)
	writeReportObjects(&b, "selectedUnits", result.SelectedUnits)
	writeReportBackendObjects(&b, "skippedObjects", result.SkippedObjects)
	if len(result.Errors) > 0 {
		b.WriteString("errors:\n")
		for _, err := range result.Errors {
			fmt.Fprintf(&b, "  - %q\n", err)
		}
	}
	return b.String()
}

func renderFluxGitAdoptionMap(opts fluxDiscoverOptions, result fluxDiscoveryResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, `schemaVersion: kapro.io/git-adoption/v1alpha1
name: %s
repoPath: %s
sourceRef: %s
units:
`, opts.Name, result.RepoPath, opts.Name)
	if len(result.SelectedUnits) == 0 {
		b.WriteString("  []\n")
		return b.String()
	}
	for _, unit := range result.SelectedUnits {
		confidence := unit.Confidence
		if confidence == "" {
			confidence = "needs-review"
		}
		fmt.Fprintf(&b, `  - name: %s
    confidence: %s
    write:
      backendKind: %s
      sourcePath: %s
      versionField: %s
    reason: %q
`, unit.Name, confidence, unit.BackendKind, unit.SourcePath, unit.VersionField, unit.Reason)
	}
	return b.String()
}

func renderFluxDiscoverReadme(opts fluxDiscoverOptions, result fluxDiscoveryResult) string {
	return fmt.Sprintf(`# Kapro Flux Discovery

This directory was generated from an existing Flux repository.

Apply observe mode first:

`+"```bash"+`
kubectl apply -f backends/%s-observe.yaml
kubectl get backend %s -o yaml
`+"```"+`

Review `+"`discovery/flux-discovery.yaml`"+`, `+"`discovery/kapro-git-map.yaml`"+`,
and `+"`sources/%s.yaml`"+` before switching the Backend from
`+"`Observe`"+` to `+"`Adopt`"+`.

Use the generated source mapping to update Git-native version fields:

`+"```bash"+`
kapro source apply --repo . --source sources/%s.yaml --set unit=revision
`+"```"+`

Kapro discovered %d Flux objects and %d Source units. Flux remains the owner
of source credentials, reconciliation, inventory, drift correction, and local
rollout behavior.
`, opts.Name, opts.Name, opts.Name, opts.Name, len(result.Objects), len(result.SelectedUnits))
}

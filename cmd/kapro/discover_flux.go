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
	Revision     string
	PathPrefixes []string
	ScanAll      bool
	Cache        bool
	MaxFiles     int
	MaxUnits     int
	Take         bool
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
	CacheStats     argoDiscoveryCacheCounters
}

func newDiscoverFluxCmd() *cobra.Command {
	opts := fluxDiscoverOptions{Cache: true, MaxFiles: defaultArgoDiscoveryMaxFiles, MaxUnits: defaultArgoDiscoveryMaxUnits}
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
	cmd.Flags().StringVar(&opts.Name, "name", "flux", "Substrate and DeliveryUnit name")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "flux-system", "Flux namespace")
	cmd.Flags().StringVar(&opts.Selector, "selector", "kapro.io/import=true", "Label selector for imported substrate objects")
	cmd.Flags().StringVar(&opts.Revision, "revision", "", "Git branch/tag/SHA when discovering a remote repository URL")
	cmd.Flags().StringSliceVar(&opts.PathPrefixes, "path-prefix", nil, "Repo path prefix to scan (repeatable; default: common Flux/GitOps paths)")
	cmd.Flags().BoolVar(&opts.ScanAll, "scan-all", false, "Scan all tracked YAML/JSON files instead of GitOps path prefixes")
	cmd.Flags().BoolVar(&opts.Cache, "cache", true, "Reuse discovery cache for unchanged Git blobs")
	cmd.Flags().IntVar(&opts.MaxFiles, "max-files", defaultArgoDiscoveryMaxFiles, "Maximum tracked YAML/JSON candidate files to parse (0 = unlimited)")
	cmd.Flags().IntVar(&opts.MaxUnits, "max-units", defaultArgoDiscoveryMaxUnits, "Maximum source mapping units to generate (0 = unlimited)")
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
	discoverPath, cleanup, err := prepareFluxDiscoverRepo(opts)
	if err != nil {
		return err
	}
	defer cleanup()
	cachePath := filepath.Join(opts.OutPath, "discovery", "flux-cache.json")
	cache := &argoDiscoveryCache{Version: 1, Files: map[string]argoCachedFile{}}
	if opts.Cache {
		cache = loadArgoDiscoveryCache(cachePath)
	}
	result, err := discoverFluxRepo(discoverPath, argoDiscoveryScanOptions{
		PathPrefixes: opts.PathPrefixes,
		ScanAll:      opts.ScanAll,
		Cache:        cache,
		MaxFiles:     opts.MaxFiles,
		MaxUnits:     opts.MaxUnits,
	})
	if err != nil {
		return err
	}
	result.RepoPath = opts.RepoPath
	if opts.Cache {
		result.CacheStats = cache.Stats
	}
	files := map[string]string{
		filepath.Join("substrates", opts.Name+discoverSubstrateFileSuffix(opts.Take)+".yaml"): renderFluxDiscoverSubstrate(opts, matchLabels),
		filepath.Join("deliveryunits", opts.Name+".yaml"):                                     renderFluxDiscoverDeliveryUnit(opts, result),
		filepath.Join("discovery", "flux-discovery.yaml"):                                     renderFluxDiscoveryReport(result),
		filepath.Join("discovery", "kapro-git-map.yaml"):                                      renderFluxGitAdoptionMap(opts, result),
		filepath.Join("discovery", "review-summary.yaml"):                                     renderDiscoveryReviewSummary("flux", opts.Name, result.RepoPath, result.SelectedUnits, result.SkippedObjects, result.Errors),
		filepath.Join("README.md"):                                                            renderFluxDiscoverReadme(opts, result),
	}
	if err := writeScaffoldFiles(opts.OutPath, files, opts.Force); err != nil {
		return err
	}
	if opts.Cache {
		if err := writeArgoDiscoveryCache(cachePath, cache); err != nil {
			return err
		}
	}
	summary := confidenceSummary(result.SelectedUnits)
	fmt.Fprintf(os.Stderr, "Discovered %d Flux objects and %d source mapping units from %s (confidence: high=%d medium=%d needs-review=%d)\n",
		len(result.Objects), len(result.SelectedUnits), opts.RepoPath, summary.High, summary.Medium, summary.NeedsReview)
	return nil
}

func prepareFluxDiscoverRepo(opts fluxDiscoverOptions) (string, func(), error) {
	return prepareGitDiscoverRepo("flux", opts.RepoPath, opts.Revision)
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
		if opts.Cache != nil {
			if cached, ok := opts.Cache.Files[file.RelPath]; ok && cached.BlobSHA == file.BlobSHA {
				replayFluxCachedFile(&result, cached)
				opts.Cache.Stats.Hits++
				continue
			}
			opts.Cache.Stats.Misses++
		}
		parsed := parseFluxDiscoveryFile(file)
		replayFluxCachedFile(&result, parsed)
		if opts.Cache != nil {
			opts.Cache.Files[file.RelPath] = parsed
		}
	}
	result.SelectedUnits = dedupeUnits(result.SelectedUnits)
	if opts.MaxUnits > 0 && len(result.SelectedUnits) > opts.MaxUnits {
		return result, fmt.Errorf("discovery found %d source mapping units, above --max-units=%d; narrow --path-prefix or review with a higher limit", len(result.SelectedUnits), opts.MaxUnits)
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

func parseFluxDiscoveryFile(file argoDiscoveryFile) argoCachedFile {
	parsed := argoCachedFile{BlobSHA: file.BlobSHA}
	if file.Size > maxArgoDiscoveryFileSize {
		parsed.SkippedObjects = append(parsed.SkippedObjects, argoDiscoveredObject{
			Kind: "File", Path: file.RelPath, Pattern: "large-file", Reason: "file exceeds discovery size limit",
		})
		return parsed
	}
	docs, err := readYAMLOrJSONDocuments(file.AbsPath)
	if err != nil {
		parsed.Errors = append(parsed.Errors, fmt.Sprintf("%s: %v", file.RelPath, err))
		return parsed
	}
	if len(docs) > 0 {
		parsed.Parsed = true
	}
	for _, doc := range docs {
		units, obj, ok := fluxUnitsFromObject(doc, file.RelPath)
		if ok {
			parsed.Objects = append(parsed.Objects, obj)
			parsed.SelectedUnits = append(parsed.SelectedUnits, units...)
			continue
		}
		if skipped, ok := skippedFluxObject(doc, file.RelPath); ok {
			parsed.SkippedObjects = append(parsed.SkippedObjects, skipped)
			continue
		}
		if units := fluxKustomizeImageUnits(doc, file.RelPath); len(units) > 0 {
			parsed.Objects = append(parsed.Objects, argoObjectFromDoc("", file.RelPath, doc))
			if parsed.Objects[len(parsed.Objects)-1].Kind == "" {
				parsed.Objects[len(parsed.Objects)-1] = argoDiscoveredObject{
					Kind: "KustomizationFile", Name: filepath.Base(file.RelPath), Path: file.RelPath, Pattern: "kustomize-images",
					Reason: "Kustomize image tags can be updated in Git",
				}
			}
			parsed.SelectedUnits = append(parsed.SelectedUnits, units...)
			continue
		}
		if units := helmChartUnits(doc, file.RelPath); len(units) > 0 {
			parsed.Objects = append(parsed.Objects, argoDiscoveredObject{
				Kind: "HelmChart", Name: filepath.Base(filepath.Dir(file.RelPath)), Path: file.RelPath, Pattern: "helm-chart",
				Reason: "Helm chart version fields can be updated in Git",
			})
			parsed.SelectedUnits = append(parsed.SelectedUnits, units...)
		}
	}
	return parsed
}

func replayFluxCachedFile(result *fluxDiscoveryResult, cached argoCachedFile) {
	if cached.Parsed {
		result.ParsedFiles++
	}
	result.Objects = append(result.Objects, cached.Objects...)
	result.SelectedUnits = append(result.SelectedUnits, cached.SelectedUnits...)
	result.SkippedObjects = append(result.SkippedObjects, cached.SkippedObjects...)
	result.Errors = append(result.Errors, cached.Errors...)
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
				Name: name, SubstrateKind: "GitYAMLField", Namespace: namespace, SourcePath: rel,
				VersionField: "spec.chart.spec.version", Confidence: "high",
				Reason: "writes Flux HelmRelease chart version in Git",
			})
		}
		if stringAt(doc, "spec", "values", "image", "tag") != "" {
			units = append(units, argoDiscoveredUnit{
				Name: name + "-image", SubstrateKind: "GitYAMLField", Namespace: namespace, SourcePath: rel,
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
		return argoDiscoveredUnit{Name: name, SubstrateKind: "GitYAMLField", Namespace: namespace, SourcePath: rel, VersionField: "spec.ref.tag", Confidence: "high", Reason: "writes Flux " + kind + " tag ref in Git"}, true
	case stringAt(doc, "spec", "ref", "semver") != "":
		return argoDiscoveredUnit{Name: name, SubstrateKind: "GitYAMLField", Namespace: namespace, SourcePath: rel, VersionField: "spec.ref.semver", Confidence: "high", Reason: "writes Flux " + kind + " semver ref in Git"}, true
	case stringAt(doc, "spec", "ref", "digest") != "":
		return argoDiscoveredUnit{Name: name, SubstrateKind: "GitYAMLField", Namespace: namespace, SourcePath: rel, VersionField: "spec.ref.digest", Confidence: "high", Reason: "writes Flux " + kind + " digest ref in Git"}, true
	case stringAt(doc, "spec", "ref", "branch") != "":
		return argoDiscoveredUnit{Name: name, SubstrateKind: "GitYAMLField", Namespace: namespace, SourcePath: rel, VersionField: "spec.ref.branch", Confidence: "medium", Reason: "writes Flux " + kind + " branch ref in Git; confirm branch promotion is intended"}, true
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
			Name: unitName, SubstrateKind: "KustomizeImage", SourcePath: rel, VersionField: imageName, Confidence: "high",
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
			Name: name + "-chart", SubstrateKind: "GitYAMLField", SourcePath: rel,
			VersionField: "version", Confidence: "medium",
			Reason: "writes Helm chart package version in Git",
		})
	}
	if stringAt(doc, "appVersion") != "" {
		units = append(units, argoDiscoveredUnit{
			Name: name + "-app", SubstrateKind: "GitYAMLField", SourcePath: rel,
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
			Name: unitName, SubstrateKind: "GitYAMLField", Namespace: namespace, SourcePath: rel,
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

func renderFluxDiscoverSubstrate(opts fluxDiscoverOptions, labels map[string]string) string {
	managementPolicy := "Observe"
	if opts.Take {
		managementPolicy = "Adopt"
	}
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha1
kind: Substrate
metadata:
  name: %s
spec:
  substrate:
    kind: flux
    actuator: flux
  execution:
    mode: hub-push
  parameters:
    namespace: %s
  discovery:
    enabled: true
    managementPolicy: %s
    maxObjects: 1000
    selector:
      matchLabels:
%s`, opts.Name, opts.Namespace, managementPolicy, renderYAMLMap(labels, 8))
}

func renderFluxDiscoverDeliveryUnit(opts fluxDiscoverOptions, result fluxDiscoveryResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, `apiVersion: kapro.io/v1alpha1
kind: DeliveryUnit
metadata:
  name: %s
  labels:
    kapro.io/unit: %s
    kapro.io/managed-by: kapro
spec:
  source:
    substrateRef: %s
    units:
`, opts.Name, opts.Name, opts.Name)
	for _, unit := range result.SelectedUnits {
		namespace := firstNonEmpty(unit.Namespace, opts.Namespace)
		fmt.Fprintf(&b, `      - name: %s
        substrateKind: %s
        namespace: %s
        sourcePath: %s
        versionField: %s
`, unit.Name, unit.SubstrateKind, namespace, unit.SourcePath, unit.VersionField)
	}
	if len(result.SelectedUnits) == 0 {
		b.WriteString("      - name: TODO\n        substrateKind: GitYAMLField\n        namespace: flux-system\n        sourcePath: path/to/flux-resource.yaml\n        versionField: spec.ref.tag\n")
	}
	return b.String()
}

func renderFluxDiscoveryReport(result fluxDiscoveryResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "repoPath: %s\nscannedFiles: %d\nparsedFiles: %d\n", result.RepoPath, result.ScannedFiles, result.ParsedFiles)
	fmt.Fprintf(&b, "objects: %d\npromotionUnits: %d\n", len(result.Objects), len(result.SelectedUnits))
	summary := confidenceSummary(result.SelectedUnits)
	fmt.Fprintf(&b, "confidence:\n  high: %d\n  medium: %d\n  needsReview: %d\n", summary.High, summary.Medium, summary.NeedsReview)
	if result.CacheStats.Hits > 0 || result.CacheStats.Misses > 0 {
		fmt.Fprintf(&b, "cache:\n  hits: %d\n  misses: %d\n", result.CacheStats.Hits, result.CacheStats.Misses)
	}
	writeReportObjects(&b, "selectedUnits", result.SelectedUnits)
	writeReportSubstrateObjects(&b, "skippedObjects", result.SkippedObjects)
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
deliveryUnitRef: %s
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
      substrateKind: %s
      sourcePath: %s
      versionField: %s
    reason: %q
`, unit.Name, confidence, unit.SubstrateKind, unit.SourcePath, unit.VersionField, unit.Reason)
	}
	return b.String()
}

func renderFluxDiscoverReadme(opts fluxDiscoverOptions, result fluxDiscoveryResult) string {
	reviewInstruction := "before switching the Substrate from `Observe` to `Adopt`"
	applyLead := "Apply observe mode first:"
	if opts.Take {
		reviewInstruction = "before running the Adopt-mode apply command below"
		applyLead = "After review, apply adopt mode:"
	}
	return fmt.Sprintf(`# Kapro Flux Discovery

This directory was generated from an existing Flux repository.

Review `+"`discovery/review-summary.yaml`"+`, `+"`discovery/flux-discovery.yaml`"+`,
`+"`discovery/kapro-git-map.yaml`"+`, and `+"`deliveryunits/%s.yaml`"+` %s.

%s

`+"```bash"+`
kubectl apply -f substrates/%s%s.yaml
kubectl get substrate %s -o yaml
`+"```"+`

Use the generated source mapping to update Git-native version fields:

`+"```bash"+`
kapro source apply --repo . --source deliveryunits/%s.yaml --set unit=revision
`+"```"+`

Kapro discovered %d Flux objects and %d source mapping units. Flux remains the owner
of source credentials, reconciliation, inventory, drift correction, and local
rollout behavior.
`, opts.Name, reviewInstruction, applyLead, opts.Name, discoverSubstrateFileSuffix(opts.Take), opts.Name, opts.Name, len(result.Objects), len(result.SelectedUnits))
}

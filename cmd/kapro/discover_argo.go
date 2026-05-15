package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const maxArgoDiscoveryFileSize = 1024 * 1024

type argoDiscoverOptions struct {
	RepoPath  string
	OutPath   string
	Name      string
	Namespace string
	Selector  string
	Force     bool
}

type argoDiscoveryResult struct {
	RepoPath        string
	ScannedFiles    int
	ParsedFiles     int
	Applications    []argoDiscoveredObject
	ApplicationSets []argoDiscoveredObject
	SelectedUnits   []argoDiscoveredUnit
	SkippedObjects  []argoDiscoveredObject
	Errors          []string
}

type argoDiscoveredObject struct {
	Kind      string
	Namespace string
	Name      string
	Path      string
	Pattern   string
	Reason    string
}

type argoDiscoveredUnit struct {
	Name         string
	BackendKind  string
	Namespace    string
	VersionField string
	SourcePath   string
	Reason       string
}

func newDiscoverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Discover existing GitOps repositories",
		Long: `Discovers existing backend-native GitOps layout and generates an
observe-first Kapro mapping. The first supported path is Argo CD brownfield
repositories using Applications, ApplicationSets, app-of-apps, and Git parameter
files.`,
	}
	cmd.AddCommand(newDiscoverArgoCmd())
	return cmd
}

func newDiscoverArgoCmd() *cobra.Command {
	var opts argoDiscoverOptions
	cmd := &cobra.Command{
		Use:   "argo [repo]",
		Short: "Discover an existing Argo CD repo and generate Kapro mapping files",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.RepoPath = "."
			if len(args) > 0 {
				opts.RepoPath = args[0]
			}
			return runArgoDiscover(opts)
		},
	}
	cmd.Flags().StringVar(&opts.OutPath, "out", "kapro-connect", "Output directory for generated Kapro files")
	cmd.Flags().StringVar(&opts.Name, "name", "argo", "BackendProfile and PromotionSource name")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "argocd", "Argo CD namespace")
	cmd.Flags().StringVar(&opts.Selector, "selector", "kapro.io/import=true", "Label selector for imported backend objects")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	return cmd
}

func runArgoDiscover(opts argoDiscoverOptions) error {
	if opts.Name == "" {
		return fmt.Errorf("--name is required")
	}
	if opts.Namespace == "" {
		opts.Namespace = "argocd"
	}
	if opts.OutPath == "" {
		opts.OutPath = "kapro-connect"
	}
	matchLabels, err := parseSelector(opts.Selector)
	if err != nil {
		return err
	}
	result, err := discoverArgoRepo(opts.RepoPath)
	if err != nil {
		return err
	}
	files := map[string]string{
		filepath.Join("backends", opts.Name+"-observe.yaml"): renderArgoDiscoverBackend(opts, matchLabels),
		filepath.Join("sources", opts.Name+".yaml"):          renderArgoDiscoverSource(opts, result),
		filepath.Join("discovery", "argo-discovery.yaml"):    renderArgoDiscoveryReport(result),
		filepath.Join("README.md"):                           renderArgoDiscoverReadme(opts, result),
	}
	if err := writeScaffoldFiles(opts.OutPath, files, opts.Force); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Discovered %d Argo Applications, %d ApplicationSets, and %d promotion units from %s\n",
		len(result.Applications), len(result.ApplicationSets), len(result.SelectedUnits), opts.RepoPath)
	return nil
}

func discoverArgoRepo(root string) (argoDiscoveryResult, error) {
	result := argoDiscoveryResult{RepoPath: root}
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			return nil
		}
		if shouldSkipDiscoveryPath(path, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		result.ScannedFiles++
		if !isDiscoveryCandidate(path) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			return nil
		}
		if info.Size() > maxArgoDiscoveryFileSize {
			result.SkippedObjects = append(result.SkippedObjects, argoDiscoveredObject{
				Kind: "File", Path: relPath(root, path), Pattern: "large-file", Reason: "file exceeds discovery size limit",
			})
			return nil
		}
		docs, err := readYAMLOrJSONDocuments(path)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", relPath(root, path), err))
			return nil
		}
		if len(docs) > 0 {
			result.ParsedFiles++
		}
		rel := relPath(root, path)
		for _, doc := range docs {
			kind := stringAt(doc, "kind")
			apiVersion := stringAt(doc, "apiVersion")
			if apiVersion != "argoproj.io/v1alpha1" {
				continue
			}
			switch kind {
			case "Application":
				app := argoObjectFromDoc(root, rel, doc)
				app.Pattern = repoApplicationPattern(root, doc)
				switch app.Pattern {
				case "app-of-apps-root":
					app.Reason = "root app packages child Applications; Kapro should normally promote child Applications or generator input files"
					result.SkippedObjects = append(result.SkippedObjects, app)
				default:
					app.Reason = "plain Argo Application can be mapped directly"
					result.Applications = append(result.Applications, app)
					result.SelectedUnits = append(result.SelectedUnits, argoDiscoveredUnit{
						Name:         argoUnitName(doc, app.Name),
						BackendKind:  "ArgoApplicationSource",
						Namespace:    app.Namespace,
						VersionField: "spec.source.targetRevision",
						SourcePath:   rel,
						Reason:       "writes Argo Application source revision",
					})
				}
			case "ApplicationSet":
				appSet := argoObjectFromDoc(root, rel, doc)
				appSet.Pattern = "applicationset"
				appSet.Reason = "ApplicationSet is a generator; Kapro should write its Git input file when possible"
				result.ApplicationSets = append(result.ApplicationSets, appSet)
				units := appSetGitFileUnits(doc, appSet.Namespace, rel)
				result.SelectedUnits = append(result.SelectedUnits, units...)
			}
		}
		return nil
	}); err != nil {
		return result, err
	}
	result.SelectedUnits = dedupeUnits(result.SelectedUnits)
	sort.Slice(result.Applications, func(i, j int) bool { return result.Applications[i].Name < result.Applications[j].Name })
	sort.Slice(result.ApplicationSets, func(i, j int) bool { return result.ApplicationSets[i].Name < result.ApplicationSets[j].Name })
	sort.Slice(result.SelectedUnits, func(i, j int) bool { return result.SelectedUnits[i].Name < result.SelectedUnits[j].Name })
	return result, nil
}

func shouldSkipDiscoveryPath(path string, d fs.DirEntry) bool {
	name := d.Name()
	switch name {
	case ".git", "node_modules", "vendor", ".terraform", "dist", "build", ".cache":
		return true
	}
	return false
}

func isDiscoveryCandidate(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml" || ext == ".json"
}

func readYAMLOrJSONDocuments(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	if strings.EqualFold(filepath.Ext(path), ".json") {
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		return []map[string]any{doc}, nil
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var docs []map[string]any
	for {
		var doc map[string]any
		if err := decoder.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if len(doc) > 0 {
			docs = append(docs, doc)
		}
	}
	return docs, nil
}

func argoObjectFromDoc(root, rel string, doc map[string]any) argoDiscoveredObject {
	return argoDiscoveredObject{
		Kind:      stringAt(doc, "kind"),
		Namespace: firstNonEmpty(stringAt(doc, "metadata", "namespace"), "argocd"),
		Name:      stringAt(doc, "metadata", "name"),
		Path:      rel,
	}
}

func repoApplicationPattern(root string, doc map[string]any) string {
	if pattern := firstNonEmpty(
		stringAt(doc, "metadata", "labels", "kapro.io/pattern"),
		stringAt(doc, "metadata", "annotations", "kapro.io/pattern"),
		stringAt(doc, "metadata", "labels", "argocd.argoproj.io/pattern"),
	); pattern != "" {
		return normalizeArgoPattern(pattern)
	}
	sourcePath := stringAt(doc, "spec", "source", "path")
	if sourcePath == "" {
		return "application"
	}
	if pathLooksLikeAppOfApps(root, sourcePath) {
		return "app-of-apps-root"
	}
	return "application"
}

func pathLooksLikeAppOfApps(root, sourcePath string) bool {
	if strings.Contains(strings.ToLower(sourcePath), "applicationset") ||
		strings.Contains(strings.ToLower(sourcePath), "applications") {
		return true
	}
	path := filepath.Join(root, sourcePath)
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || !isDiscoveryCandidate(entry.Name()) {
			continue
		}
		docs, err := readYAMLOrJSONDocuments(filepath.Join(path, entry.Name()))
		if err != nil {
			continue
		}
		for _, doc := range docs {
			if stringAt(doc, "apiVersion") == "argoproj.io/v1alpha1" {
				switch stringAt(doc, "kind") {
				case "Application", "ApplicationSet":
					return true
				}
			}
		}
	}
	return false
}

func appSetGitFileUnits(doc map[string]any, namespace, rel string) []argoDiscoveredUnit {
	var units []argoDiscoveredUnit
	var inputs []string
	versionVariables := appSetTargetRevisionVariables(doc)
	for _, generator := range sliceAt(doc, "spec", "generators") {
		for _, filePath := range gitFileGeneratorPaths(generator) {
			inputs = append(inputs, filePath)
			if len(versionVariables) == 0 {
				continue
			}
			for _, unitName := range listGeneratorAppNames(generator) {
				for _, versionVariable := range versionVariables {
					units = append(units, argoDiscoveredUnit{
						Name:         unitName,
						BackendKind:  backendKindForPath(filePath),
						Namespace:    namespace,
						VersionField: fmt.Sprintf("%s:%s", filePath, versionVariable),
						SourcePath:   rel,
						Reason:       "ApplicationSet Git file generator targetRevision comes from this input field",
					})
				}
			}
		}
	}
	if len(units) == 0 && len(versionVariables) > 0 {
		name := stringAt(doc, "metadata", "labels", "app.kubernetes.io/name")
		if name == "" {
			name = stringAt(doc, "metadata", "name")
		}
		for _, filePath := range inputs {
			for _, versionVariable := range versionVariables {
				units = append(units, argoDiscoveredUnit{
					Name:         name,
					BackendKind:  backendKindForPath(filePath),
					Namespace:    namespace,
					VersionField: fmt.Sprintf("%s:%s", filePath, versionVariable),
					SourcePath:   rel,
					Reason:       "ApplicationSet Git file generator targetRevision comes from this input field",
				})
			}
		}
	}
	return units
}

func appSetTargetRevisionVariables(doc map[string]any) []string {
	seen := map[string]struct{}{}
	add := func(raw string) {
		variable := templateVariableName(raw)
		if variable == "" || ignoredApplicationSetVariable(variable) {
			return
		}
		seen[variable] = struct{}{}
	}
	add(stringAt(doc, "spec", "template", "spec", "source", "targetRevision"))
	for _, source := range sliceAt(doc, "spec", "template", "spec", "sources") {
		add(stringAtValue(source, "targetRevision"))
	}
	variables := make([]string, 0, len(seen))
	for variable := range seen {
		variables = append(variables, variable)
	}
	sort.Strings(variables)
	return variables
}

func ignoredApplicationSetVariable(variable string) bool {
	switch variable {
	case "branch", "repoUrl", "env", "namespace", "cluster", "server":
		return true
	default:
		return false
	}
}

func gitFileGeneratorPaths(generator any) []string {
	var paths []string
	for _, git := range nestedGeneratorMaps(generator, "git") {
		for _, file := range sliceAt(git, "files") {
			if path := stringAtValue(file, "path"); path != "" {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

func listGeneratorAppNames(generator any) []string {
	var names []string
	for _, list := range nestedGeneratorMaps(generator, "list") {
		for _, element := range sliceAt(list, "elements") {
			if name := firstNonEmpty(stringAtValue(element, "appName"), stringAtValue(element, "name"), stringAtValue(element, "service")); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

func nestedGeneratorMaps(value any, key string) []any {
	m, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	var found []any
	if direct, ok := m[key]; ok {
		found = append(found, direct)
	}
	if matrix, ok := m["matrix"].(map[string]any); ok {
		for _, child := range sliceAt(matrix, "generators") {
			found = append(found, nestedGeneratorMaps(child, key)...)
		}
	}
	return found
}

func backendKindForPath(path string) string {
	switch strings.ToLower(filepath.Ext(strings.TrimSuffix(path, "*"))) {
	case ".json":
		return "GitJSONField"
	case ".yaml", ".yml":
		return "GitYAMLField"
	default:
		return "GitParameterField"
	}
}

func templateVariableName(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "{{")
	value = strings.TrimSuffix(value, "}}")
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, ".")
	if strings.ContainsAny(value, " |/{}") {
		return ""
	}
	return value
}

func normalizeArgoPattern(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "app-of-apps", "appofapps", "app-of-apps-root", "root":
		return "app-of-apps-root"
	case "app-of-apps-child", "appofapps-child", "child":
		return "app-of-apps-child"
	case "applicationset-child", "appset-child":
		return "applicationset-child"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func dedupeUnits(units []argoDiscoveredUnit) []argoDiscoveredUnit {
	seen := map[string]argoDiscoveredUnit{}
	for _, unit := range units {
		if unit.Name == "" {
			continue
		}
		key := unit.Name + "\x00" + unit.VersionField
		seen[key] = unit
	}
	deduped := make([]argoDiscoveredUnit, 0, len(seen))
	for _, unit := range seen {
		deduped = append(deduped, unit)
	}
	return deduped
}

func renderArgoDiscoverBackend(opts argoDiscoverOptions, labels map[string]string) string {
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha1
kind: BackendProfile
metadata:
  name: %s
spec:
  driver: argo
  runtime: Hub
  parameters:
    namespace: %s
  discovery:
    enabled: true
    managementPolicy: Observe
    selector:
      matchLabels:
%s`, opts.Name, opts.Namespace, renderYAMLMap(labels, 8))
}

func renderArgoDiscoverSource(opts argoDiscoverOptions, result argoDiscoveryResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, `apiVersion: kapro.io/v1alpha1
kind: PromotionSource
metadata:
  name: %s
spec:
  backendRef: %s
  units:
`, opts.Name, opts.Name)
	for _, unit := range result.SelectedUnits {
		fmt.Fprintf(&b, `    - name: %s
      backendKind: %s
      namespace: %s
      versionField: %s
`, unit.Name, unit.BackendKind, unit.Namespace, unit.VersionField)
	}
	if len(result.SelectedUnits) == 0 {
		b.WriteString("    - name: TODO\n      backendKind: ArgoApplicationSource\n      namespace: argocd\n      versionField: spec.source.targetRevision\n")
	}
	return b.String()
}

func renderArgoDiscoveryReport(result argoDiscoveryResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "repoPath: %s\nscannedFiles: %d\nparsedFiles: %d\n", result.RepoPath, result.ScannedFiles, result.ParsedFiles)
	fmt.Fprintf(&b, "applications: %d\napplicationSets: %d\npromotionUnits: %d\n", len(result.Applications), len(result.ApplicationSets), len(result.SelectedUnits))
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

func writeReportObjects(b *strings.Builder, key string, units []argoDiscoveredUnit) {
	b.WriteString(key + ":\n")
	if len(units) == 0 {
		b.WriteString("  []\n")
		return
	}
	for _, unit := range units {
		fmt.Fprintf(b, "  - name: %s\n    backendKind: %s\n    namespace: %s\n    versionField: %s\n    sourcePath: %s\n    reason: %q\n",
			unit.Name, unit.BackendKind, unit.Namespace, unit.VersionField, unit.SourcePath, unit.Reason)
	}
}

func writeReportBackendObjects(b *strings.Builder, key string, objects []argoDiscoveredObject) {
	b.WriteString(key + ":\n")
	if len(objects) == 0 {
		b.WriteString("  []\n")
		return
	}
	for _, obj := range objects {
		fmt.Fprintf(b, "  - kind: %s\n    namespace: %s\n    name: %s\n    path: %s\n    pattern: %s\n    reason: %q\n",
			obj.Kind, obj.Namespace, obj.Name, obj.Path, obj.Pattern, obj.Reason)
	}
}

func renderArgoDiscoverReadme(opts argoDiscoverOptions, result argoDiscoveryResult) string {
	return fmt.Sprintf(`# Kapro Argo Discovery

This directory was generated from an existing Argo CD repository.

Apply observe mode first:

`+"```bash"+`
kubectl apply -f backends/%s-observe.yaml
kubectl get backendprofile %s -o yaml
`+"```"+`

Review `+"`discovery/argo-discovery.yaml`"+` and `+"`sources/%s.yaml`"+` before switching
the BackendProfile from `+"`Observe`"+` to `+"`Adopt`"+`.

Kapro discovered %d Applications, %d ApplicationSets, and %d promotion units.
Argo CD remains the owner of cluster credentials, repository credentials, sync
policy, and local rollout behavior.
`, opts.Name, opts.Name, opts.Name, len(result.Applications), len(result.ApplicationSets), len(result.SelectedUnits))
}

func argoUnitName(doc map[string]any, fallback string) string {
	return firstNonEmpty(
		stringAt(doc, "metadata", "labels", "kapro.io/unit"),
		stringAt(doc, "metadata", "labels", "service"),
		stringAt(doc, "metadata", "labels", "app.kubernetes.io/name"),
		fallback,
	)
}

func stringAt(doc map[string]any, path ...string) string {
	var cur any = doc
	for _, segment := range path {
		returnValue, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = returnValue[segment]
	}
	if s, ok := cur.(string); ok {
		return s
	}
	return ""
}

func stringAtValue(value any, path ...string) string {
	m, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	return stringAt(m, path...)
}

func sliceAt(value any, path ...string) []any {
	cur := value
	for _, segment := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[segment]
	}
	if items, ok := cur.([]any); ok {
		return items
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func relPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

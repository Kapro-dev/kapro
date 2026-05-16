package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const maxArgoDiscoveryFileSize = 1024 * 1024
const defaultArgoDiscoveryMaxFiles = 10000
const defaultArgoDiscoveryMaxUnits = 1000

var defaultArgoDiscoveryPrefixes = []string{"argocd", "apps", "clusters", "environments", "flux"}

type argoDiscoverOptions struct {
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
	Force        bool
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
	CacheStats      argoDiscoveryCacheCounters
}

type argoDiscoveryScanOptions struct {
	PathPrefixes []string
	ScanAll      bool
	Cache        *argoDiscoveryCache
	MaxFiles     int
	MaxUnits     int
}

type argoDiscoveryCache struct {
	Version int                        `json:"version"`
	Files   map[string]argoCachedFile  `json:"files"`
	Stats   argoDiscoveryCacheCounters `json:"stats,omitempty"`
}

type argoDiscoveryCacheCounters struct {
	Hits   int `json:"hits,omitempty"`
	Misses int `json:"misses,omitempty"`
}

type argoCachedFile struct {
	BlobSHA         string                 `json:"blobSHA"`
	Parsed          bool                   `json:"parsed,omitempty"`
	Applications    []argoDiscoveredObject `json:"applications,omitempty"`
	ApplicationSets []argoDiscoveredObject `json:"applicationSets,omitempty"`
	SelectedUnits   []argoDiscoveredUnit   `json:"selectedUnits,omitempty"`
	SkippedObjects  []argoDiscoveredObject `json:"skippedObjects,omitempty"`
	Errors          []string               `json:"errors,omitempty"`
}

type argoDiscoveryFile struct {
	RelPath string
	AbsPath string
	BlobSHA string
	Size    int64
}

type argoDiscoveredObject struct {
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	Path      string `json:"path,omitempty"`
	Pattern   string `json:"pattern,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type argoDiscoveredUnit struct {
	Name         string `json:"name,omitempty"`
	BackendKind  string `json:"backendKind,omitempty"`
	Namespace    string `json:"namespace,omitempty"`
	VersionField string `json:"versionField,omitempty"`
	SourcePath   string `json:"sourcePath,omitempty"`
	Confidence   string `json:"confidence,omitempty"`
	Reason       string `json:"reason,omitempty"`
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
	opts := argoDiscoverOptions{Cache: true, MaxFiles: defaultArgoDiscoveryMaxFiles, MaxUnits: defaultArgoDiscoveryMaxUnits}
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
	cmd.Flags().StringVar(&opts.Revision, "revision", "", "Git branch/tag/SHA when discovering a remote repository URL")
	cmd.Flags().StringSliceVar(&opts.PathPrefixes, "path-prefix", nil, "Repo path prefix to scan (repeatable; default: argocd, apps, clusters, environments, flux)")
	cmd.Flags().BoolVar(&opts.ScanAll, "scan-all", false, "Scan all tracked YAML/JSON files instead of GitOps path prefixes")
	cmd.Flags().BoolVar(&opts.Cache, "cache", true, "Reuse discovery cache for unchanged Git blobs")
	cmd.Flags().IntVar(&opts.MaxFiles, "max-files", defaultArgoDiscoveryMaxFiles, "Maximum tracked YAML/JSON candidate files to parse (0 = unlimited)")
	cmd.Flags().IntVar(&opts.MaxUnits, "max-units", defaultArgoDiscoveryMaxUnits, "Maximum promotion units to generate (0 = unlimited)")
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
	discoverPath, cleanup, err := prepareArgoDiscoverRepo(opts)
	if err != nil {
		return err
	}
	defer cleanup()
	cachePath := filepath.Join(opts.OutPath, "discovery", "argo-cache.json")
	cache := &argoDiscoveryCache{Version: 1, Files: map[string]argoCachedFile{}}
	if opts.Cache {
		cache = loadArgoDiscoveryCache(cachePath)
	}
	result, err := discoverArgoRepo(discoverPath, argoDiscoveryScanOptions{
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
	files := map[string]string{
		filepath.Join("backends", opts.Name+"-observe.yaml"): renderArgoDiscoverBackend(opts, matchLabels),
		filepath.Join("sources", opts.Name+".yaml"):          renderArgoDiscoverSource(opts, result),
		filepath.Join("discovery", "argo-discovery.yaml"):    renderArgoDiscoveryReport(result),
		filepath.Join("discovery", "kapro-git-map.yaml"):     renderArgoGitAdoptionMap(opts, result),
		filepath.Join("README.md"):                           renderArgoDiscoverReadme(opts, result),
	}
	if err := writeScaffoldFiles(opts.OutPath, files, opts.Force); err != nil {
		return err
	}
	if opts.Cache {
		result.CacheStats = cache.Stats
		if err := writeArgoDiscoveryCache(cachePath, cache); err != nil {
			return err
		}
	}
	summary := confidenceSummary(result.SelectedUnits)
	fmt.Fprintf(os.Stderr, "Discovered %d Argo Applications, %d ApplicationSets, and %d promotion units from %s (confidence: high=%d medium=%d needs-review=%d)\n",
		len(result.Applications), len(result.ApplicationSets), len(result.SelectedUnits), opts.RepoPath, summary.High, summary.Medium, summary.NeedsReview)
	return nil
}

func prepareArgoDiscoverRepo(opts argoDiscoverOptions) (string, func(), error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", func() {}, fmt.Errorf("kapro discover argo requires git CLI in PATH")
	}
	if !looksLikeGitRemote(opts.RepoPath) {
		root, err := gitWorktreeRoot(opts.RepoPath)
		if err != nil {
			return "", func() {}, err
		}
		return root, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "kapro-discover-argo-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temp clone dir: %w", err)
	}
	args := []string{"clone", "--depth=1"}
	if opts.Revision != "" {
		args = append(args, "--branch", opts.Revision)
	}
	args = append(args, opts.RepoPath, dir)
	cmd := exec.Command("git", args...)
	cmd.Env = cleanGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		return "", func() {}, fmt.Errorf("clone %s: %w\n%s", opts.RepoPath, err, strings.TrimSpace(string(out)))
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

func looksLikeGitRemote(path string) bool {
	return strings.Contains(path, "://") || strings.HasPrefix(path, "git@") || strings.Contains(path, ":")
}

func gitWorktreeRoot(path string) (string, error) {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	cmd.Env = cleanGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s is not a Git worktree; kapro discover argo requires a Git checkout\n%s", path, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func discoverArgoRepo(root string, opts argoDiscoveryScanOptions) (argoDiscoveryResult, error) {
	result := argoDiscoveryResult{RepoPath: root}
	files, err := gitTrackedDiscoveryFiles(root, opts)
	if err != nil {
		return result, err
	}
	for _, file := range files {
		result.ScannedFiles++
		if opts.Cache != nil {
			if cached, ok := opts.Cache.Files[file.RelPath]; ok && cached.BlobSHA == file.BlobSHA {
				replayArgoCachedFile(&result, cached)
				opts.Cache.Stats.Hits++
				continue
			}
			opts.Cache.Stats.Misses++
		}
		parsed := parseArgoDiscoveryFile(root, file)
		replayArgoCachedFile(&result, parsed)
		if opts.Cache != nil {
			opts.Cache.Files[file.RelPath] = parsed
		}
	}
	result.SelectedUnits = dedupeUnits(result.SelectedUnits)
	if opts.MaxUnits > 0 && len(result.SelectedUnits) > opts.MaxUnits {
		return result, fmt.Errorf("discovery found %d promotion units, above --max-units=%d; narrow --path-prefix or review with a higher limit", len(result.SelectedUnits), opts.MaxUnits)
	}
	sort.Slice(result.Applications, func(i, j int) bool { return result.Applications[i].Name < result.Applications[j].Name })
	sort.Slice(result.ApplicationSets, func(i, j int) bool { return result.ApplicationSets[i].Name < result.ApplicationSets[j].Name })
	sort.Slice(result.SelectedUnits, func(i, j int) bool { return result.SelectedUnits[i].Name < result.SelectedUnits[j].Name })
	return result, nil
}

func gitTrackedDiscoveryFiles(root string, opts argoDiscoveryScanOptions) ([]argoDiscoveryFile, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "-s", "-z")
	cmd.Env = cleanGitEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files failed in %s: %w", root, err)
	}
	prefixes := opts.PathPrefixes
	if len(prefixes) == 0 && !opts.ScanAll {
		prefixes = defaultArgoDiscoveryPrefixes
	}
	var files []argoDiscoveryFile
	for _, entry := range bytes.Split(out, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		header, relRaw, ok := bytes.Cut(entry, []byte{'\t'})
		if !ok {
			continue
		}
		parts := strings.Fields(string(header))
		if len(parts) < 2 {
			continue
		}
		rel := filepath.ToSlash(string(relRaw))
		if !opts.ScanAll && !hasDiscoveryPrefix(rel, prefixes) {
			continue
		}
		if !isDiscoveryCandidate(rel) {
			continue
		}
		if opts.MaxFiles > 0 && len(files) >= opts.MaxFiles {
			return nil, fmt.Errorf("discovery candidate file limit exceeded (--max-files=%d); narrow --path-prefix or raise the limit", opts.MaxFiles)
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(abs)
		if err != nil {
			continue
		}
		if info.Size() > maxArgoDiscoveryFileSize {
			files = append(files, argoDiscoveryFile{RelPath: rel, AbsPath: abs, BlobSHA: parts[1], Size: info.Size()})
			continue
		}
		files = append(files, argoDiscoveryFile{RelPath: rel, AbsPath: abs, BlobSHA: parts[1], Size: info.Size()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelPath < files[j].RelPath })
	return files, nil
}

func hasDiscoveryPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		prefix = strings.Trim(filepath.ToSlash(prefix), "/")
		if prefix == "" {
			continue
		}
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func parseArgoDiscoveryFile(root string, file argoDiscoveryFile) argoCachedFile {
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
		kind := stringAt(doc, "kind")
		apiVersion := stringAt(doc, "apiVersion")
		if apiVersion != "argoproj.io/v1alpha1" {
			continue
		}
		switch kind {
		case "Application":
			app := argoObjectFromDoc(root, file.RelPath, doc)
			app.Pattern = repoApplicationPattern(root, doc)
			switch app.Pattern {
			case "app-of-apps-root":
				app.Reason = "root app packages child Applications; Kapro should normally promote child Applications or generator input files"
				parsed.SkippedObjects = append(parsed.SkippedObjects, app)
			default:
				app.Reason = "plain Argo Application can be mapped directly"
				versionField := applicationVersionField(doc)
				parsed.Applications = append(parsed.Applications, app)
				parsed.SelectedUnits = append(parsed.SelectedUnits, argoDiscoveredUnit{
					Name:         argoUnitName(doc, app.Name),
					BackendKind:  "ArgoApplicationSource",
					Namespace:    app.Namespace,
					VersionField: versionField,
					SourcePath:   file.RelPath,
					Confidence:   "high",
					Reason:       "writes Argo Application source revision",
				})
			}
		case "ApplicationSet":
			appSet := argoObjectFromDoc(root, file.RelPath, doc)
			appSet.Pattern = "applicationset"
			appSet.Reason = "ApplicationSet is a generator; Kapro should write its Git input file when possible"
			parsed.ApplicationSets = append(parsed.ApplicationSets, appSet)
			units := appSetGitFileUnits(doc, appSet.Namespace, file.RelPath)
			parsed.SelectedUnits = append(parsed.SelectedUnits, units...)
		}
	}
	return parsed
}

func applicationVersionField(doc map[string]any) string {
	if stringAt(doc, "spec", "source", "targetRevision") != "" {
		return "spec.source.targetRevision"
	}
	for i, source := range sliceAt(doc, "spec", "sources") {
		if stringAtValue(source, "targetRevision") != "" {
			return fmt.Sprintf("spec.sources[%d].targetRevision", i)
		}
	}
	return "spec.source.targetRevision"
}

func replayArgoCachedFile(result *argoDiscoveryResult, cached argoCachedFile) {
	if cached.Parsed {
		result.ParsedFiles++
	}
	result.Applications = append(result.Applications, cached.Applications...)
	result.ApplicationSets = append(result.ApplicationSets, cached.ApplicationSets...)
	result.SelectedUnits = append(result.SelectedUnits, cached.SelectedUnits...)
	result.SkippedObjects = append(result.SkippedObjects, cached.SkippedObjects...)
	result.Errors = append(result.Errors, cached.Errors...)
}

func loadArgoDiscoveryCache(path string) *argoDiscoveryCache {
	cache := &argoDiscoveryCache{Version: 1, Files: map[string]argoCachedFile{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return cache
	}
	if err := json.Unmarshal(data, cache); err != nil || cache.Version != 1 {
		return &argoDiscoveryCache{Version: 1, Files: map[string]argoCachedFile{}}
	}
	if cache.Files == nil {
		cache.Files = map[string]argoCachedFile{}
	}
	cache.Stats = argoDiscoveryCacheCounters{}
	return cache
}

func writeArgoDiscoveryCache(path string, cache *argoDiscoveryCache) error {
	if cache == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create discovery cache dir: %w", err)
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal discovery cache: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write discovery cache: %w", err)
	}
	return nil
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
						Confidence:   "high",
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
					Confidence:   "medium",
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
	if merge, ok := m["merge"].(map[string]any); ok {
		for _, child := range sliceAt(merge, "generators") {
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
    maxObjects: 1000
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
      sourcePath: %s
      versionField: %s
`, unit.Name, unit.BackendKind, unit.Namespace, unit.SourcePath, unit.VersionField)
	}
	if len(result.SelectedUnits) == 0 {
		b.WriteString("    - name: TODO\n      backendKind: ArgoApplicationSource\n      namespace: argocd\n      sourcePath: path/to/application.yaml\n      versionField: spec.source.targetRevision\n")
	}
	return b.String()
}

func renderArgoDiscoveryReport(result argoDiscoveryResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "repoPath: %s\nscannedFiles: %d\nparsedFiles: %d\n", result.RepoPath, result.ScannedFiles, result.ParsedFiles)
	fmt.Fprintf(&b, "applications: %d\napplicationSets: %d\npromotionUnits: %d\n", len(result.Applications), len(result.ApplicationSets), len(result.SelectedUnits))
	summary := confidenceSummary(result.SelectedUnits)
	fmt.Fprintf(&b, "confidence:\n  high: %d\n  medium: %d\n  needsReview: %d\n", summary.High, summary.Medium, summary.NeedsReview)
	if result.CacheStats.Hits > 0 || result.CacheStats.Misses > 0 {
		fmt.Fprintf(&b, "cache:\n  hits: %d\n  misses: %d\n", result.CacheStats.Hits, result.CacheStats.Misses)
	}
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

type argoConfidenceSummary struct {
	High        int
	Medium      int
	NeedsReview int
}

func confidenceSummary(units []argoDiscoveredUnit) argoConfidenceSummary {
	var summary argoConfidenceSummary
	for _, unit := range units {
		switch unit.Confidence {
		case "high":
			summary.High++
		case "medium":
			summary.Medium++
		default:
			summary.NeedsReview++
		}
	}
	return summary
}

func renderArgoGitAdoptionMap(opts argoDiscoverOptions, result argoDiscoveryResult) string {
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

func writeReportObjects(b *strings.Builder, key string, units []argoDiscoveredUnit) {
	b.WriteString(key + ":\n")
	if len(units) == 0 {
		b.WriteString("  []\n")
		return
	}
	for _, unit := range units {
		confidence := unit.Confidence
		if confidence == "" {
			confidence = "needs-review"
		}
		fmt.Fprintf(b, "  - name: %s\n    backendKind: %s\n    namespace: %s\n    versionField: %s\n    sourcePath: %s\n    confidence: %s\n    reason: %q\n",
			unit.Name, unit.BackendKind, unit.Namespace, unit.VersionField, unit.SourcePath, confidence, unit.Reason)
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

Review `+"`discovery/argo-discovery.yaml`"+`, `+"`discovery/kapro-git-map.yaml`"+`,
and `+"`sources/%s.yaml`"+` before switching the BackendProfile from
`+"`Observe`"+` to `+"`Adopt`"+`.

Use the generated source mapping to update Git-native version fields:

`+"```bash"+`
kapro source apply --repo . --source sources/%s.yaml --set unit=revision
`+"```"+`

Kapro discovered %d Applications, %d ApplicationSets, and %d promotion units.
Argo CD remains the owner of cluster credentials, repository credentials, sync
policy, and local rollout behavior.
`, opts.Name, opts.Name, opts.Name, opts.Name, len(result.Applications), len(result.ApplicationSets), len(result.SelectedUnits))
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

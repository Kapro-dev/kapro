package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	yamlv3 "go.yaml.in/yaml/v3"
	sigsyaml "sigs.k8s.io/yaml"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

type sourceApplyOptions struct {
	RepoPath   string
	SourcePath string
	Version    string
	VersionSet []string
	Include    []string
	All        bool
	DryRun     bool
}

func newSourceApplyCmd() *cobra.Command {
	var opts sourceApplyOptions
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply PromotionSource version mappings to a Git checkout",
		Long: `Updates repo-native YAML or JSON version fields from a PromotionSource.

This is the Git-native brownfield path: Kapro writes only explicitly mapped
fields, then users review and commit the Git diff. If a mapping expands to
multiple files, pass --include for the intended file(s), or --all when the same
revision must be applied to every matched file.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runSourceApply(opts)
		},
	}
	cmd.Flags().StringVar(&opts.RepoPath, "repo", ".", "Git checkout root")
	cmd.Flags().StringVar(&opts.SourcePath, "source", "", "PromotionSource YAML file (required)")
	cmd.Flags().StringVar(&opts.Version, "version", "", "Default revision for every mapped unit")
	cmd.Flags().StringArrayVar(&opts.VersionSet, "set", nil, "Per-unit revision (repeatable: --set unit=revision)")
	cmd.Flags().StringArrayVar(&opts.Include, "include", nil, "Repo-relative file glob to allow when a mapping matches multiple files")
	cmd.Flags().BoolVar(&opts.All, "all", false, "Apply glob mappings to every matched file")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Print planned writes without changing files")
	_ = cmd.MarkFlagRequired("source")
	return cmd
}

func runSourceApply(opts sourceApplyOptions) error {
	source, err := readPromotionSourceFile(opts.SourcePath)
	if err != nil {
		return err
	}
	versions, err := parseReleaseVersions(opts.VersionSet)
	if err != nil {
		return err
	}
	if opts.Version == "" && len(versions) == 0 {
		return fmt.Errorf("--version or at least one --set unit=revision is required")
	}
	if opts.All && len(opts.Include) > 0 {
		return fmt.Errorf("--all and --include are mutually exclusive")
	}

	unitNames := make(map[string]struct{}, len(source.Spec.Units))
	for _, unit := range source.Spec.Units {
		unitNames[unit.Name] = struct{}{}
	}
	for unit := range versions {
		if _, ok := unitNames[unit]; !ok {
			return fmt.Errorf("--set references unknown unit %q", unit)
		}
	}

	plan := make([]sourceWrite, 0)
	for _, unit := range source.Spec.Units {
		version := versions[unit.Name]
		if version == "" {
			version = opts.Version
		}
		if version == "" {
			continue
		}
		writes, err := planUnitSourceWrites(opts, unit, version)
		if err != nil {
			return fmt.Errorf("unit %q: %w", unit.Name, err)
		}
		plan = append(plan, writes...)
	}
	plan, err = dedupeSourceWrites(plan)
	if err != nil {
		return err
	}
	if len(plan) == 0 {
		return fmt.Errorf("no mapped units matched the requested versions")
	}

	sort.Slice(plan, func(i, j int) bool {
		if plan[i].Path == plan[j].Path {
			return plan[i].Field < plan[j].Field
		}
		return plan[i].Path < plan[j].Path
	})
	for _, write := range plan {
		if opts.DryRun {
			fmt.Printf("would update %s:%s -> %s\n", write.Path, write.Field, write.Version)
			continue
		}
		if err := updateStructuredField(write.AbsPath, write.Field, write.Version); err != nil {
			return err
		}
		fmt.Printf("updated %s:%s -> %s\n", write.Path, write.Field, write.Version)
	}
	return nil
}

func dedupeSourceWrites(writes []sourceWrite) ([]sourceWrite, error) {
	seen := make(map[string]sourceWrite, len(writes))
	for _, write := range writes {
		key := write.Path + "\x00" + write.Field
		if existing, ok := seen[key]; ok {
			if existing.Version != write.Version {
				return nil, fmt.Errorf("conflicting writes for %s:%s (%s and %s)", write.Path, write.Field, existing.Version, write.Version)
			}
			continue
		}
		seen[key] = write
	}
	deduped := make([]sourceWrite, 0, len(seen))
	for _, write := range seen {
		deduped = append(deduped, write)
	}
	return deduped, nil
}

type sourceWrite struct {
	Path    string
	AbsPath string
	Field   string
	Version string
}

func readPromotionSourceFile(path string) (*kaprov1alpha1.PromotionSource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read source %s: %w", path, err)
	}
	var source kaprov1alpha1.PromotionSource
	if err := sigsyaml.Unmarshal(data, &source); err != nil {
		return nil, fmt.Errorf("parse PromotionSource %s: %w", path, err)
	}
	if source.Kind != "" && source.Kind != "PromotionSource" {
		return nil, fmt.Errorf("source %s is kind %q, expected PromotionSource", path, source.Kind)
	}
	if len(source.Spec.Units) == 0 {
		return nil, fmt.Errorf("PromotionSource %s has no units", path)
	}
	return &source, nil
}

func planUnitSourceWrites(opts sourceApplyOptions, unit kaprov1alpha1.PromotionUnit, version string) ([]sourceWrite, error) {
	pattern, field, err := unitWriteTarget(unit)
	if err != nil {
		return nil, err
	}
	paths, err := resolveRepoPaths(opts.RepoPath, pattern)
	if err != nil {
		return nil, err
	}
	paths = filterIncludedPaths(paths, opts.Include)
	if len(paths) == 0 {
		return nil, fmt.Errorf("mapping %q matched no files", pattern)
	}
	if len(paths) > 1 && !opts.All && len(opts.Include) == 0 {
		return nil, fmt.Errorf("mapping %q matched %d files; use --include or --all", pattern, len(paths))
	}

	writes := make([]sourceWrite, 0, len(paths))
	for _, rel := range paths {
		abs, err := safeRepoPath(opts.RepoPath, rel)
		if err != nil {
			return nil, err
		}
		writes = append(writes, sourceWrite{Path: rel, AbsPath: abs, Field: field, Version: version})
	}
	return writes, nil
}

func unitWriteTarget(unit kaprov1alpha1.PromotionUnit) (string, string, error) {
	if file, field, ok := strings.Cut(unit.VersionField, ":"); ok {
		if strings.TrimSpace(file) == "" || strings.TrimSpace(field) == "" {
			return "", "", fmt.Errorf("versionField must use file:field, got %q", unit.VersionField)
		}
		return strings.TrimSpace(file), strings.TrimSpace(field), nil
	}
	if unit.SourcePath == "" {
		return "", "", fmt.Errorf("sourcePath is required when versionField does not include file:field")
	}
	if unit.VersionField == "" {
		return "", "", fmt.Errorf("versionField is required")
	}
	return unit.SourcePath, unit.VersionField, nil
}

func resolveRepoPaths(repo, pattern string) ([]string, error) {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	if pattern == "" {
		return nil, fmt.Errorf("empty file path")
	}
	if hasParentPathSegment(pattern) {
		return nil, fmt.Errorf("file path %q must stay inside repo", pattern)
	}
	if !hasGlobMeta(pattern) {
		abs, err := safeRepoPath(repo, pattern)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, err
		}
		return []string{pattern}, nil
	}
	absPattern, err := safeRepoPath(repo, pattern)
	if err != nil {
		return nil, err
	}
	matches, err := filepath.Glob(absPattern)
	if err != nil {
		return nil, fmt.Errorf("glob %q: %w", pattern, err)
	}
	repoAbs, err := filepath.Abs(repo)
	if err != nil {
		return nil, err
	}
	relMatches := make([]string, 0, len(matches))
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil || info.IsDir() {
			continue
		}
		rel, err := filepath.Rel(repoAbs, match)
		if err != nil {
			return nil, err
		}
		relMatches = append(relMatches, filepath.ToSlash(rel))
	}
	sort.Strings(relMatches)
	return relMatches, nil
}

func hasGlobMeta(path string) bool {
	return strings.ContainsAny(path, "*?[")
}

func hasParentPathSegment(path string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func filterIncludedPaths(paths, includes []string) []string {
	if len(includes) == 0 {
		return paths
	}
	var filtered []string
	for _, path := range paths {
		for _, include := range includes {
			matched, err := filepath.Match(filepath.ToSlash(include), path)
			if err == nil && matched {
				filtered = append(filtered, path)
				break
			}
		}
	}
	return filtered
}

func safeRepoPath(repo, rel string) (string, error) {
	repoAbs, err := filepath.Abs(repo)
	if err != nil {
		return "", err
	}
	pathAbs, err := filepath.Abs(filepath.Join(repoAbs, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	if pathAbs != repoAbs && !strings.HasPrefix(pathAbs, repoAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes repo", rel)
	}
	return pathAbs, nil
}

func updateStructuredField(path, field, value string) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return updateJSONField(path, field, value)
	case ".yaml", ".yml":
		return updateYAMLField(path, field, value)
	default:
		return fmt.Errorf("unsupported file extension for %s", path)
	}
}

func updateJSONField(path, field, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var doc any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return fmt.Errorf("parse JSON %s: %w", path, err)
	}
	if err := setStructuredValue(&doc, parseFieldPath(field), value); err != nil {
		return fmt.Errorf("set %s:%s: %w", path, field, err)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON %s: %w", path, err)
	}
	out = append(out, '\n')
	return os.WriteFile(path, out, 0644)
}

func updateYAMLField(path, field, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var doc yamlv3.Node
	if err := yamlv3.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse YAML %s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		return fmt.Errorf("YAML %s is empty", path)
	}
	if err := setYAMLValue(doc.Content[0], parseFieldPath(field), value); err != nil {
		return fmt.Errorf("set %s:%s: %w", path, field, err)
	}
	out, err := yamlv3.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshal YAML %s: %w", path, err)
	}
	return os.WriteFile(path, out, 0644)
}

type fieldToken struct {
	Name  string
	Index *int
}

func parseFieldPath(field string) []fieldToken {
	parts := strings.Split(field, ".")
	tokens := make([]fieldToken, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		token := fieldToken{Name: part}
		if start := strings.Index(part, "["); start >= 0 && strings.HasSuffix(part, "]") {
			token.Name = part[:start]
			rawIndex := strings.TrimSuffix(part[start+1:], "]")
			if idx, err := strconv.Atoi(rawIndex); err == nil {
				token.Index = &idx
			}
		}
		tokens = append(tokens, token)
	}
	return tokens
}

func setStructuredValue(cur *any, tokens []fieldToken, value string) error {
	if len(tokens) == 0 {
		*cur = value
		return nil
	}
	token := tokens[0]
	m, ok := (*cur).(map[string]any)
	if !ok {
		return fmt.Errorf("expected object at %q", token.Name)
	}
	next, ok := m[token.Name]
	if !ok {
		if len(tokens) == 1 {
			m[token.Name] = value
			return nil
		}
		next = map[string]any{}
		m[token.Name] = next
	}
	if token.Index != nil {
		items, ok := next.([]any)
		if !ok {
			return fmt.Errorf("expected array at %q", token.Name)
		}
		if *token.Index < 0 || *token.Index >= len(items) {
			return fmt.Errorf("index %d out of range at %q", *token.Index, token.Name)
		}
		if len(tokens) == 1 {
			items[*token.Index] = value
			m[token.Name] = items
			return nil
		}
		child := items[*token.Index]
		if err := setStructuredValue(&child, tokens[1:], value); err != nil {
			return err
		}
		items[*token.Index] = child
		m[token.Name] = items
		return nil
	}
	if len(tokens) == 1 {
		m[token.Name] = value
		return nil
	}
	if err := setStructuredValue(&next, tokens[1:], value); err != nil {
		return err
	}
	m[token.Name] = next
	return nil
}

func setYAMLValue(cur *yamlv3.Node, tokens []fieldToken, value string) error {
	if len(tokens) == 0 {
		cur.Kind = yamlv3.ScalarNode
		cur.Tag = "!!str"
		cur.Value = value
		return nil
	}
	if cur.Kind != yamlv3.MappingNode {
		return fmt.Errorf("expected mapping at %q", tokens[0].Name)
	}
	valueNode := yamlMappingValue(cur, tokens[0].Name)
	if valueNode == nil {
		valueNode = &yamlv3.Node{Kind: yamlv3.MappingNode, Tag: "!!map"}
		cur.Content = append(cur.Content,
			&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: tokens[0].Name},
			valueNode,
		)
	}
	if tokens[0].Index != nil {
		if valueNode.Kind != yamlv3.SequenceNode {
			return fmt.Errorf("expected sequence at %q", tokens[0].Name)
		}
		idx := *tokens[0].Index
		if idx < 0 || idx >= len(valueNode.Content) {
			return fmt.Errorf("index %d out of range at %q", idx, tokens[0].Name)
		}
		if len(tokens) == 1 {
			valueNode.Content[idx] = &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: value}
			return nil
		}
		return setYAMLValue(valueNode.Content[idx], tokens[1:], value)
	}
	if len(tokens) == 1 {
		valueNode.Kind = yamlv3.ScalarNode
		valueNode.Tag = "!!str"
		valueNode.Value = value
		valueNode.Content = nil
		return nil
	}
	return setYAMLValue(valueNode, tokens[1:], value)
}

func yamlMappingValue(node *yamlv3.Node, key string) *yamlv3.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

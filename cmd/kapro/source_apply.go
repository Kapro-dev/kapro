package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	sigsyaml "sigs.k8s.io/yaml"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/internal/writetarget"
)

const maxSourceApplyFileSize = 10 * 1024 * 1024

type sourceApplyOptions struct {
	RepoPath   string
	SourcePath string
	Version    string
	VersionSet []string
	Include    []string
	All        bool
	DryRun     bool
	Commit     bool
	Push       bool
	Message    string
}

func newSourceApplyCmd() *cobra.Command {
	var opts sourceApplyOptions
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply Source version mappings to a Git checkout",
		Long: `Updates repo-native YAML or JSON version fields from a Source.

This is the Git-native existing-repo path: Kapro writes only explicitly mapped
fields, then users review and commit the Git diff. If a mapping expands to
multiple files, pass --include for the intended file(s), or --all when the same
revision must be applied to every matched file.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runSourceApply(opts)
		},
	}
	cmd.Flags().StringVar(&opts.RepoPath, "repo", ".", "Git checkout root")
	cmd.Flags().StringVar(&opts.SourcePath, "source", "", "Source YAML file (required)")
	cmd.Flags().StringVar(&opts.Version, "version", "", "Default revision for every mapped unit")
	cmd.Flags().StringArrayVar(&opts.VersionSet, "set", nil, "Per-unit revision (repeatable: --set unit=revision)")
	cmd.Flags().StringArrayVar(&opts.Include, "include", nil, "Repo-relative file glob to allow when a mapping matches multiple files")
	cmd.Flags().BoolVar(&opts.All, "all", false, "Apply glob mappings to every matched file")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Print planned writes without changing files")
	cmd.Flags().BoolVar(&opts.Commit, "commit", false, "Create a Git commit after applying changes")
	cmd.Flags().BoolVar(&opts.Push, "push", false, "Push the current branch after committing changes")
	cmd.Flags().StringVar(&opts.Message, "message", "", "Git commit message for --commit")
	_ = cmd.MarkFlagRequired("source")
	return cmd
}

func runSourceApply(opts sourceApplyOptions) error {
	repoRoot, err := gitWorktreeRoot(opts.RepoPath)
	if err != nil {
		return err
	}
	opts.RepoPath = repoRoot
	source, err := readPromotionSourceFile(opts.SourcePath)
	if err != nil {
		return err
	}
	versions, err := parsePromotionRunVersions(opts.VersionSet)
	if err != nil {
		return err
	}
	if opts.Version == "" && len(versions) == 0 {
		return fmt.Errorf("--version or at least one --set unit=revision is required")
	}
	if opts.All && len(opts.Include) > 0 {
		return fmt.Errorf("--all and --include are mutually exclusive")
	}
	if opts.DryRun && (opts.Commit || opts.Push) {
		return fmt.Errorf("--dry-run cannot be combined with --commit or --push")
	}
	if opts.Push && !opts.Commit {
		return fmt.Errorf("--push requires --commit")
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
		}
	}
	if !opts.DryRun {
		if err := applySourceWritesAtomically(plan); err != nil {
			return err
		}
		for _, write := range plan {
			fmt.Printf("updated %s:%s -> %s\n", write.Path, write.Field, write.Version)
		}
	}
	if opts.Commit {
		if opts.Message == "" {
			opts.Message = "Update Kapro promotion source revisions"
		}
		if err := commitSourceWrites(opts.RepoPath, plan, opts.Message, opts.Push); err != nil {
			return err
		}
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
	Kind    string
	Field   string
	Version string
}

func readPromotionSourceFile(path string) (*kaprov1alpha1.Source, error) {
	data, err := readFileLimited(path, maxSourceApplyFileSize)
	if err != nil {
		return nil, fmt.Errorf("read source %s: %w", path, err)
	}
	var source kaprov1alpha1.Source
	if err := sigsyaml.Unmarshal(data, &source); err != nil {
		return nil, fmt.Errorf("parse source %s: %w", path, err)
	}
	if source.Kind != "" && source.Kind != "Source" {
		return nil, fmt.Errorf("source %s is kind %q, expected Source", path, source.Kind)
	}
	if len(source.Spec.Units) == 0 {
		return nil, fmt.Errorf("source %s has no units", path)
	}
	return &source, nil
}

func planUnitSourceWrites(opts sourceApplyOptions, unit kaprov1alpha1.Unit, version string) ([]sourceWrite, error) {
	pattern, field, err := unitWriteTarget(unit)
	if err != nil {
		return nil, err
	}
	paths, err := resolveRepoPaths(opts.RepoPath, pattern)
	if err != nil {
		return nil, err
	}
	if hasGlobMeta(pattern) {
		paths = filterIncludedPaths(paths, opts.Include)
	}
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
		writes = append(writes, sourceWrite{Path: rel, AbsPath: abs, Kind: unit.SubstrateKind, Field: field, Version: version})
	}
	return writes, nil
}

func unitWriteTarget(unit kaprov1alpha1.Unit) (string, string, error) {
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
	files, err := gitTrackedRepoFiles(repo)
	if err != nil {
		return nil, err
	}
	var matches []string
	for _, file := range files {
		if hasGlobMeta(pattern) {
			matched, err := filepath.Match(pattern, file)
			if err == nil && matched {
				matches = append(matches, file)
			}
			continue
		}
		if file == pattern {
			matches = append(matches, file)
		}
	}
	sort.Strings(matches)
	return matches, nil
}

func gitTrackedRepoFiles(repo string) ([]string, error) {
	cmd := exec.Command("git", "-C", repo, "ls-files", "-z")
	cmd.Env = cleanGitEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files failed in %s: %w", repo, err)
	}
	var files []string
	for _, raw := range strings.Split(string(out), "\x00") {
		if raw == "" {
			continue
		}
		files = append(files, filepath.ToSlash(raw))
	}
	return files, nil
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
	repoResolved, err := filepath.EvalSymlinks(repoAbs)
	if err != nil {
		return "", fmt.Errorf("resolve repo root %s: %w", repo, err)
	}
	pathAbs, err := filepath.Abs(filepath.Join(repoAbs, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	if pathAbs != repoAbs && !strings.HasPrefix(pathAbs, repoAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes repo", rel)
	}
	info, err := os.Lstat(pathAbs)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", rel, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("path %q is a symlink; refusing to write through symlink", rel)
	}
	pathResolved, err := filepath.EvalSymlinks(pathAbs)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", rel, err)
	}
	if pathResolved != repoResolved && !strings.HasPrefix(pathResolved, repoResolved+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q resolves outside repo", rel)
	}
	return pathAbs, nil
}

func updateStructuredField(path, field, value string) error {
	return writetarget.UpdateStructuredField(path, field, value)
}

func applySourceWrite(write sourceWrite) error {
	if write.Kind == "KustomizeImage" {
		imageName, imageNewName := kustomizeImageField(write.Field)
		if imageName == "" {
			return fmt.Errorf("KustomizeImage versionField must be image name or imageName:newName, got %q", write.Field)
		}
		return writetarget.UpdateKustomizeImage(write.AbsPath, imageName, imageNewName, write.Version)
	}
	return writetarget.UpdateStructuredField(write.AbsPath, write.Field, write.Version)
}

func applySourceWritesAtomically(writes []sourceWrite) error {
	tmpDir, err := os.MkdirTemp("", "kapro-source-apply-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	tempPaths := make(map[string]string, len(writes))
	modes := make(map[string]os.FileMode, len(writes))
	for _, write := range writes {
		if _, ok := tempPaths[write.Path]; ok {
			continue
		}
		info, err := os.Stat(write.AbsPath)
		if err != nil {
			return fmt.Errorf("stat %s: %w", write.Path, err)
		}
		data, err := readFileLimited(write.AbsPath, maxSourceApplyFileSize)
		if err != nil {
			return fmt.Errorf("read %s: %w", write.Path, err)
		}
		tmpPath := filepath.Join(tmpDir, strings.ReplaceAll(write.Path, "/", "__"))
		if err := os.WriteFile(tmpPath, data, info.Mode().Perm()); err != nil {
			return fmt.Errorf("stage %s: %w", write.Path, err)
		}
		tempPaths[write.Path] = tmpPath
		modes[write.Path] = info.Mode().Perm()
	}

	for _, write := range writes {
		staged := write
		staged.AbsPath = tempPaths[write.Path]
		if err := applySourceWrite(staged); err != nil {
			return err
		}
	}

	for _, write := range writes {
		tmpPath := tempPaths[write.Path]
		if tmpPath == "" {
			continue
		}
		data, err := readFileLimited(tmpPath, maxSourceApplyFileSize)
		if err != nil {
			return fmt.Errorf("read staged %s: %w", write.Path, err)
		}
		if err := os.WriteFile(write.AbsPath, data, modes[write.Path]); err != nil {
			return fmt.Errorf("write %s: %w", write.Path, err)
		}
		delete(tempPaths, write.Path)
	}
	return nil
}

func kustomizeImageField(field string) (string, string) {
	imageName, newName, _ := strings.Cut(field, ":")
	return strings.TrimSpace(imageName), strings.TrimSpace(newName)
}

func commitSourceWrites(repo string, writes []sourceWrite, message string, push bool) error {
	seen := map[string]struct{}{}
	args := []string{"add", "--"}
	for _, write := range writes {
		if _, ok := seen[write.Path]; ok {
			continue
		}
		seen[write.Path] = struct{}{}
		args = append(args, write.Path)
	}
	if err := runGit(repo, args...); err != nil {
		return err
	}
	if err := runGit(repo, "commit", "-m", message); err != nil {
		return err
	}
	if push {
		if err := runGit(repo, "push"); err != nil {
			return err
		}
	}
	return nil
}

func runGit(repo string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = cleanGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	if len(out) > 0 {
		fmt.Fprint(os.Stderr, string(out))
	}
	return nil
}

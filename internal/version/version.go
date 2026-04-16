// Package version holds build-time version information injected by goreleaser ldflags.
package version

var (
	// Version is the semantic version (e.g. v0.1.0).
	Version = "dev"
	// Commit is the git commit SHA.
	Commit = "unknown"
	// Date is the build date.
	Date = "unknown"
)

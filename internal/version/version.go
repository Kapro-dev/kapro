// Package version holds build-time version information injected by gopromotionrunr ldflags.
package version

var (
	// Version is the semantic version (e.g. v0.6.0).
	Version = "dev"
	// Commit is the git commit SHA.
	Commit = "unknown"
	// Date is the build date.
	Date = "unknown"
)

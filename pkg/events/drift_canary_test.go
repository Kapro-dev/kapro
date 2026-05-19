package events_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"kapro.io/kapro/pkg/events"
)

// TestEventTypesDocumentedInCloudEventsMd is the drift canary: every
// EventType constant exported from pkg/events MUST appear verbatim
// somewhere in docs/cloudevents.md. Adding a new constant without
// adding a row to the vocabulary table will fail this test, which is
// the single line of CI defence against the docs↔code drift bugs that
// cost PRs #80 and #81 a combined 20 review comments.
//
// The check is deliberately strict-substring (the entire EventType
// literal must appear). False negatives — "I documented it under a
// slightly different spelling" — are wins for the canary.
func TestEventTypesDocumentedInCloudEventsMd(t *testing.T) {
	docs, err := readDocsFile("cloudevents.md")
	if err != nil {
		t.Fatalf("read docs/cloudevents.md: %v", err)
	}
	for _, et := range events.AllEventTypes() {
		if !strings.Contains(docs, string(et)) {
			t.Errorf("EventType %q is exported from pkg/events but not documented in docs/cloudevents.md.\n"+
				"Add a row to the matching scope table; see docs/CONTRIBUTING-events.md step 1.",
				string(et))
		}
	}
}

// TestRenderSucceedsForEveryEventType exercises pkg/events.Render
// against every constant in the vocabulary. A future contributor who
// adds a constant whose Render path panics (e.g. nil-deref on a new
// required field) gets a clean test-fail instead of a runtime crash
// in the dispatcher.
func TestRenderSucceedsForEveryEventType(t *testing.T) {
	for _, et := range events.AllEventTypes() {
		ev := events.Event{
			Type:          et,
			PromotionName: "canary",
			Phase:         "Progressing",
		}
		body, env, err := events.Render(ev)
		if err != nil {
			t.Errorf("Render(%q): %v", et, err)
			continue
		}
		if env.Type != et {
			t.Errorf("Render(%q).Type = %q, want %q", et, env.Type, et)
		}
		if len(body) == 0 {
			t.Errorf("Render(%q): zero-length body", et)
		}
		if !strings.Contains(string(body), string(et)) {
			t.Errorf("Render(%q) body does not include the type string", et)
		}
	}
}

// readDocsFile resolves a path relative to the repository root by
// walking up from the test file's directory. This makes the canary
// robust against `go test` being invoked from any working directory.
func readDocsFile(name string) (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", os.ErrNotExist
	}
	dir := filepath.Dir(thisFile)
	for range 6 {
		candidate := filepath.Join(dir, "docs", name)
		if _, err := os.Stat(candidate); err == nil {
			b, err := os.ReadFile(candidate)
			if err != nil {
				return "", err
			}
			return string(b), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", os.ErrNotExist
}

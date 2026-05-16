package writetarget

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateKustomizeImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kustomization.yaml")
	if err := os.WriteFile(path, []byte(`resources:
- deploy.yaml
images:
- name: example.com/api
  newTag: old
`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := UpdateKustomizeImage(path, "example.com/api", "", "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "newTag: v1.2.3") {
		t.Fatalf("image tag was not updated:\n%s", got)
	}
}

func TestUpdateStructuredFieldYAMLSequence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte(`spec:
  sources:
  - targetRevision: old
`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := UpdateStructuredField(path, "spec.sources[0].targetRevision", "main"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "targetRevision: main") {
		t.Fatalf("target revision was not updated:\n%s", got)
	}
}

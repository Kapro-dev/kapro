package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileLimitedRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.yaml")
	if err := os.WriteFile(path, []byte("12345"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readFileLimited(path, 4); err == nil || !strings.Contains(err.Error(), "file exceeds size limit") {
		t.Fatalf("err=%v, want size-limit error", err)
	}
}

func TestReadYAMLOrJSONDocumentsRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.yaml")
	data := append([]byte("apiVersion: v1\nkind: ConfigMap\n"), make([]byte, maxArgoDiscoveryFileSize)...)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readYAMLOrJSONDocuments(path); err == nil || !strings.Contains(err.Error(), "file exceeds size limit") {
		t.Fatalf("err=%v, want discovery size-limit error", err)
	}
}

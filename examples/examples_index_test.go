package examples_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

func TestExamplesTopLevelChaptersAreIndexed(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name())
		}
	}
	sort.Strings(dirs)
	want := []string{
		"00-deliveryunit-lessons",
		"01-quickstarts",
		"02-plans",
		"03-triggers",
		"04-substrates",
		"05-plugins",
		"06-sdk-go",
		"07-actuator-hello-world",
		"08-monitoring",
		"09-archive",
		"10-kind-demo",
		"11-rbac",
	}
	if len(dirs) != len(want) {
		t.Fatalf("top-level example chapters = %v, want %v", dirs, want)
	}
	for i, dir := range dirs {
		if dir != want[i] {
			t.Fatalf("top-level example chapters = %v, want %v", dirs, want)
		}
		if errs := validation.IsDNS1123Label(dir); len(errs) > 0 {
			t.Fatalf("example chapter %q should be a DNS label: %s", dir, strings.Join(errs, "; "))
		}
		if index := dir[:2]; index != fmt.Sprintf("%02d", i) {
			t.Fatalf("example chapter %q has index %q, want %02d", dir, index, i)
		}
	}
}

func TestEveryExampleDirectoryHasReadme(t *testing.T) {
	if err := filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") {
			return filepath.SkipDir
		}
		readme := filepath.Join(path, "README.md")
		if _, err := os.Stat(readme); err != nil {
			t.Errorf("%s is missing README.md", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEveryExampleReadmeHasRunnableGuidance(t *testing.T) {
	keywords := []string{
		"```bash",
		"```sh",
		"kubectl ",
		"go run ",
		"go test ",
		"docker ",
		"kind ",
		"helm ",
		"scripts/",
		"./examples/",
		"fluent-bit ",
		"vector ",
		"validate",
	}
	if err := filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "README.md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := strings.ToLower(string(data))
		for _, keyword := range keywords {
			if strings.Contains(text, keyword) {
				return nil
			}
		}
		t.Errorf("%s is missing runnable/apply/validate guidance", path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

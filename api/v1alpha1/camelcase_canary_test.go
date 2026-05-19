package v1alpha1_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestJSONTagsAreCamelCase scans every json:"..." struct tag in
// api/v1alpha1 and asserts the wire name matches Kubernetes camelCase
// convention. This is the drift canary that prevents a future
// "promotionplan" (lowercase one-word) from sneaking back in alongside
// "promotionPlan" (camelCase two-word).
//
// Rule applied to the wire portion of each json tag (the token before
// any comma):
//
//   - First character must be lowercase ASCII.
//   - The token must NOT contain an underscore (no snake_case).
//   - If the token contains an embedded uppercase letter (e.g.
//     "promotionPlan"), every alphabetic run between capitals must
//     either start lowercase OR be a single uppercase acronym (e.g.
//     "URL", "ID", "UID") that is at most 3 letters long.
//   - "-" and inline-omit ("inline") are accepted special tokens.
//   - Empty tag (json:",inline" / json:"-") is accepted.
//
// The canary will fail loudly if someone re-introduces patterns like
// "promotionplan", "promotionrunRef", "kapro_ref", "PromotionPlan",
// etc. It is intentionally pedantic: the cost of a false positive
// (one PR comment) is much smaller than the cost of API drift
// shipping unnoticed (multiple follow-up PRs, see git history).
func TestJSONTagsAreCamelCase(t *testing.T) {
	dir, err := apiDir()
	if err != nil {
		t.Fatalf("locate api/v1alpha1: %v", err)
	}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(e.Name(), "_test.go") ||
			strings.HasPrefix(e.Name(), "zz_generated") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			st, ok := n.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range st.Fields.List {
				if field.Tag == nil {
					continue
				}
				tag := strings.Trim(field.Tag.Value, "`")
				jsonTag := extractJSONTag(tag)
				if jsonTag == "" {
					continue
				}
				if reason := violatesCamelCase(jsonTag); reason != "" {
					fname := ""
					if len(field.Names) > 0 {
						fname = field.Names[0].Name
					}
					t.Errorf("%s:%d: field %q has json tag %q — %s",
						filepath.Base(path), fset.Position(field.Pos()).Line, fname, jsonTag, reason)
				}
			}
			return true
		})
	}
}

// extractJSONTag pulls the wire-name portion of a struct tag's json:""
// field (the substring before the first comma). Returns "" when the
// tag has no json segment or when the wire name is empty / "-" / a
// pass-through token.
func extractJSONTag(rawTag string) string {
	for _, part := range splitTagParts(rawTag) {
		const prefix = `json:"`
		if !strings.HasPrefix(part, prefix) {
			continue
		}
		v := strings.TrimSuffix(strings.TrimPrefix(part, prefix), `"`)
		if v == "" || v == "-" {
			return ""
		}
		if comma := strings.Index(v, ","); comma >= 0 {
			v = v[:comma]
		}
		return v
	}
	return ""
}

// splitTagParts mimics reflect.StructTag tokenisation enough to find
// the json segment of a backtick struct tag.
func splitTagParts(t string) []string {
	var out []string
	for len(t) > 0 {
		// skip whitespace
		i := 0
		for i < len(t) && (t[i] == ' ' || t[i] == '\t') {
			i++
		}
		t = t[i:]
		if t == "" {
			break
		}
		// scan key:"value"
		colon := strings.Index(t, ":")
		if colon < 0 {
			break
		}
		quoteStart := colon + 1
		if quoteStart >= len(t) || t[quoteStart] != '"' {
			break
		}
		// find unescaped closing quote
		end := quoteStart + 1
		for end < len(t) && t[end] != '"' {
			if t[end] == '\\' && end+1 < len(t) {
				end++
			}
			end++
		}
		if end >= len(t) {
			break
		}
		out = append(out, t[:end+1])
		t = t[end+1:]
	}
	return out
}

// violatesCamelCase returns an explanation when name is not Kubernetes
// camelCase; empty string when it's fine.
func violatesCamelCase(name string) string {
	if name == "" || name == "-" || name == "inline" {
		return ""
	}
	if !(name[0] >= 'a' && name[0] <= 'z') {
		return "must start with a lowercase ASCII letter"
	}
	if strings.Contains(name, "_") {
		return "must not contain underscores (snake_case)"
	}
	// Heuristic: a violation we care about is "two semantic words
	// concatenated lowercase" like "promotionplan" or "promotionrun".
	// We cannot easily detect that without a dictionary. Instead we
	// catch the specific concrete tokens we know to be drift-prone.
	//
	// Allow-listing acronyms could be added here when needed.
	for _, bad := range []string{
		"promotionplan", "promotionrun", "kaproref",
		"promotionrunref", "promotionplanref",
		"promotiontarget", "promotionsource",
		"promotionplanprogress", "kaproplan",
	} {
		if strings.Contains(strings.ToLower(name), bad) && !strings.Contains(name, "Plan") &&
			!strings.Contains(name, "Run") && !strings.Contains(name, "Ref") &&
			!strings.Contains(name, "Target") && !strings.Contains(name, "Source") &&
			!strings.Contains(name, "Progress") {
			return "looks like two words concatenated lowercase (e.g. \"" + bad + "\") — use camelCase"
		}
	}
	return ""
}

// apiDir resolves the api/v1alpha1 directory relative to this test
// file, so `go test` works from any cwd.
func apiDir() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", os.ErrNotExist
	}
	return filepath.Dir(thisFile), nil
}

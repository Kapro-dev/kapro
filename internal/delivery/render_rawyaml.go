package delivery

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// RawYAMLRenderer parses every *.yaml / *.yml file in the artifact and
// returns them as ordered Objects.
//
// Order: files are visited in lexical-sort order of their artifact-relative
// paths. Within a file, multi-document YAML order is preserved. This makes
// it possible for an artifact to declare "apply namespace first, then
// secrets, then deployments" by prefixing filenames with 00-, 10-, 20-,
// etc. — same convention `kubectl apply -f dir` uses.
//
// Duplicates: if two documents resolve to the same Object.Key, the later
// one in iteration order wins silently. The "last writer wins" rule
// matches kubectl apply semantics — repeated declarations of the same
// object in different files are common (helpers.tpl style includes,
// app-of-apps templates) and rejecting them would force authors to write
// brittle de-duplication logic upstream.
//
// A future commit may add an optional strict-mode that surfaces a warning
// when colliding documents differ byte-for-byte; the spoke binary doesn't
// need it yet because the OCI artifacts are produced by Kapro's own
// promotion pipeline.
type RawYAMLRenderer struct{}

// Render parses the filesystem and returns RenderedManifests with
// Format=FormatRawYAML.
func (RawYAMLRenderer) Render(ctx context.Context, pa *PulledArtifact, _ RenderOptions) (RenderedManifests, error) {
	if pa == nil || pa.FS == nil {
		return RenderedManifests{}, fmt.Errorf("nil pulled artifact")
	}

	files, err := collectYAMLFiles(pa.FS)
	if err != nil {
		return RenderedManifests{}, err
	}

	out := make([]*Object, 0, len(files))
	seen := map[string]int{} // Object.Key → out[] index
	for _, f := range files {
		raw, err := fs.ReadFile(pa.FS, f)
		if err != nil {
			return RenderedManifests{}, fmt.Errorf("read %s: %w", f, err)
		}
		docs, err := splitYAMLDocs(raw)
		if err != nil {
			return RenderedManifests{}, fmt.Errorf("split YAML %s: %w", f, err)
		}
		for i, doc := range docs {
			obj, err := parseUnstructured(doc, f, i)
			if err != nil {
				return RenderedManifests{}, err
			}
			if obj == nil {
				continue
			}
			key := obj.Key()
			if existing, ok := seen[key]; ok {
				out[existing] = obj
				continue
			}
			seen[key] = len(out)
			out = append(out, obj)
		}
	}
	return RenderedManifests{Objects: out, Format: FormatRawYAML}, nil
}

// collectYAMLFiles returns artifact-relative paths of every *.yaml / *.yml
// regular file, sorted lexically. Hidden files (".") and the well-known
// kustomization.yaml at any depth are excluded — the latter belongs to the
// kustomize renderer.
func collectYAMLFiles(fsys fs.FS) ([]string, error) {
	var files []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := pathBase(p)
		if strings.HasPrefix(base, ".") {
			return nil
		}
		if base == "kustomization.yaml" || base == "kustomization.yml" || base == "Kustomization" {
			return nil
		}
		lower := strings.ToLower(p)
		if strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func pathBase(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// splitYAMLDocs splits a YAML stream into individual documents on `---`
// boundaries. Tolerates leading/trailing whitespace and an absent final
// terminator. Empty documents are dropped — callers see only non-empty
// chunks.
func splitYAMLDocs(raw []byte) ([][]byte, error) {
	var docs [][]byte
	cur := bytes.NewBuffer(nil)
	flush := func() {
		if len(bytes.TrimSpace(cur.Bytes())) > 0 {
			docs = append(docs, append([]byte(nil), cur.Bytes()...))
		}
		cur.Reset()
	}
	for _, line := range splitLines(raw) {
		if isDocSeparator(line) {
			flush()
			continue
		}
		cur.Write(line)
		cur.WriteByte('\n')
	}
	flush()
	return docs, nil
}

// splitLines splits raw into lines, normalising "\r\n" → "\n". No empty
// trailing line is included when raw ends in a newline.
func splitLines(raw []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\n' {
			continue
		}
		end := i
		if end > start && raw[end-1] == '\r' {
			end--
		}
		out = append(out, raw[start:end])
		start = i + 1
	}
	if start < len(raw) {
		out = append(out, raw[start:])
	}
	return out
}

func isDocSeparator(line []byte) bool {
	s := bytes.TrimSpace(line)
	return bytes.HasPrefix(s, []byte("---")) && (len(s) == 3 || s[3] == ' ' || s[3] == '#')
}

// parseUnstructured decodes a single YAML document into an unstructured
// Kubernetes object. Returns (nil, nil) for empty / comment-only documents
// — callers should skip these.
func parseUnstructured(doc []byte, source string, ordinal int) (*Object, error) {
	if len(bytes.TrimSpace(doc)) == 0 {
		return nil, nil
	}
	u := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(doc, &u.Object); err != nil {
		return nil, fmt.Errorf("parse %s doc #%d: %w", source, ordinal, err)
	}
	if len(u.Object) == 0 {
		return nil, nil
	}
	if u.GetKind() == "" {
		return nil, fmt.Errorf("parse %s doc #%d: missing kind", source, ordinal)
	}
	if u.GetAPIVersion() == "" {
		return nil, fmt.Errorf("parse %s doc #%d: missing apiVersion", source, ordinal)
	}
	return FromUnstructured(u, fmt.Sprintf("%s#%d", source, ordinal)), nil
}

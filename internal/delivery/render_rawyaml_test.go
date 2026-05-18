package delivery

import (
	"context"
	"testing"
	"testing/fstest"
)

func TestSplitYAMLDocs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"single doc", "kind: A\n", 1},
		{"two docs", "kind: A\n---\nkind: B\n", 2},
		{"trailing separator", "kind: A\n---\n", 1},
		{"leading separator", "---\nkind: A\n", 1},
		{"crlf endings", "kind: A\r\n---\r\nkind: B\r\n", 2},
		{"separator with comment", "kind: A\n--- # comment\nkind: B\n", 2},
		{"empty docs dropped", "---\n---\nkind: A\n---\n", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docs, err := splitYAMLDocs([]byte(tc.in))
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if len(docs) != tc.want {
				t.Fatalf("got %d docs, want %d (docs=%q)", len(docs), tc.want, docs)
			}
		})
	}
}

func TestRawYAMLRenderer_Render_BasicTwoFiles(t *testing.T) {
	pa := &PulledArtifact{
		FS: fstest.MapFS{
			"10-ns.yaml": &fstest.MapFile{Data: []byte(`
apiVersion: v1
kind: Namespace
metadata:
  name: demo
`)},
			"20-cm.yaml": &fstest.MapFile{Data: []byte(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: demo
data:
  key: value
`)},
		},
	}
	r := RawYAMLRenderer{}
	out, err := r.Render(context.Background(), pa, RenderOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out.Format != FormatRawYAML {
		t.Fatalf("format = %s, want %s", out.Format, FormatRawYAML)
	}
	if len(out.Objects) != 2 {
		t.Fatalf("got %d objects, want 2", len(out.Objects))
	}
	if out.Objects[0].GVK.Kind != "Namespace" {
		t.Fatalf("first object Kind=%s, want Namespace", out.Objects[0].GVK.Kind)
	}
	if out.Objects[1].GVK.Kind != "ConfigMap" {
		t.Fatalf("second object Kind=%s, want ConfigMap", out.Objects[1].GVK.Kind)
	}
}

func TestRawYAMLRenderer_Render_MultiDocSingleFile(t *testing.T) {
	pa := &PulledArtifact{
		FS: fstest.MapFS{
			"all.yaml": &fstest.MapFile{Data: []byte(`
apiVersion: v1
kind: Namespace
metadata:
  name: demo
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: a
  namespace: demo
data: {key: v}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: b
  namespace: demo
data: {key: v}
`)},
		},
	}
	out, err := RawYAMLRenderer{}.Render(context.Background(), pa, RenderOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(out.Objects) != 3 {
		t.Fatalf("got %d objects, want 3", len(out.Objects))
	}
}

func TestRawYAMLRenderer_Render_LaterDuplicateWins(t *testing.T) {
	pa := &PulledArtifact{
		FS: fstest.MapFS{
			"a.yaml": &fstest.MapFile{Data: []byte(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: dup
  namespace: ns
data:
  key: first
`)},
			"b.yaml": &fstest.MapFile{Data: []byte(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: dup
  namespace: ns
data:
  key: second
`)},
		},
	}
	out, err := RawYAMLRenderer{}.Render(context.Background(), pa, RenderOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(out.Objects) != 1 {
		t.Fatalf("got %d objects, want 1", len(out.Objects))
	}
	data, _, _ := unstructuredString(out.Objects[0], "data", "key")
	if data != "second" {
		t.Fatalf("late-wins: got %q, want %q", data, "second")
	}
}

func TestRawYAMLRenderer_Render_SkipsKustomization(t *testing.T) {
	pa := &PulledArtifact{
		FS: fstest.MapFS{
			"kustomization.yaml": &fstest.MapFile{Data: []byte(`resources: [x.yaml]`)},
			"x.yaml": &fstest.MapFile{Data: []byte(`
apiVersion: v1
kind: Namespace
metadata:
  name: demo
`)},
		},
	}
	out, err := RawYAMLRenderer{}.Render(context.Background(), pa, RenderOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(out.Objects) != 1 {
		t.Fatalf("got %d objects, want 1 (kustomization.yaml should be skipped)", len(out.Objects))
	}
}

func TestRawYAMLRenderer_Render_MissingKindRejected(t *testing.T) {
	pa := &PulledArtifact{
		FS: fstest.MapFS{
			"bad.yaml": &fstest.MapFile{Data: []byte(`
apiVersion: v1
metadata:
  name: noKind
`)},
		},
	}
	_, err := RawYAMLRenderer{}.Render(context.Background(), pa, RenderOptions{})
	if err == nil {
		t.Fatal("expected error for missing kind")
	}
}

func unstructuredString(o *Object, path ...string) (string, bool, error) {
	m := o.U.Object
	for i, p := range path {
		v, ok := m[p]
		if !ok {
			return "", false, nil
		}
		if i == len(path)-1 {
			s, ok := v.(string)
			return s, ok, nil
		}
		m, ok = v.(map[string]any)
		if !ok {
			return "", false, nil
		}
	}
	return "", false, nil
}

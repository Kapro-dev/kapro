package delivery

import (
	"testing"
	"testing/fstest"
)

func TestDetectFormat_ByMediaType(t *testing.T) {
	cases := []struct {
		mt   string
		want Format
	}{
		{MediaTypeHelmChartContent, FormatHelm},
		{MediaTypeKustomize, FormatKustomize},
		{MediaTypeRawYAML, FormatRawYAML},
	}
	for _, tc := range cases {
		t.Run(tc.mt, func(t *testing.T) {
			pa := &PulledArtifact{
				FS:        fstest.MapFS{"placeholder.yaml": &fstest.MapFile{Data: []byte("kind: X\napiVersion: v1\n")}},
				MediaType: tc.mt,
			}
			got, err := DetectFormat(pa)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestDetectFormat_ByStructure(t *testing.T) {
	cases := []struct {
		name string
		fs   fstest.MapFS
		want Format
	}{
		{
			"helm via Chart.yaml",
			fstest.MapFS{
				"Chart.yaml":               &fstest.MapFile{Data: []byte("apiVersion: v2\nname: x\n")},
				"templates/configmap.yaml": &fstest.MapFile{Data: []byte("kind: ConfigMap\n")},
			},
			FormatHelm,
		},
		{
			"kustomize via kustomization.yaml",
			fstest.MapFS{
				"kustomization.yaml": &fstest.MapFile{Data: []byte("resources: []\n")},
				"deploy.yaml":        &fstest.MapFile{Data: []byte("kind: Deployment\n")},
			},
			FormatKustomize,
		},
		{
			"raw-yaml fallback",
			fstest.MapFS{
				"a.yaml": &fstest.MapFile{Data: []byte("kind: A\n")},
			},
			FormatRawYAML,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DetectFormat(&PulledArtifact{FS: tc.fs})
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestDetectFormat_EmptyArtifactErrors(t *testing.T) {
	_, err := DetectFormat(&PulledArtifact{FS: fstest.MapFS{}})
	if err == nil {
		t.Fatal("expected error for empty artifact")
	}
}

func TestDetectFormat_NilInputErrors(t *testing.T) {
	if _, err := DetectFormat(nil); err == nil {
		t.Fatal("expected error for nil pulled artifact")
	}
}

package delivery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io/fs"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
)

// buildOCITestArtifact pushes a single-layer OCI artifact (tar.gz wrapping
// the supplied files) into an in-memory store and tags it. Returns the
// store and the tag the puller should reference.
func buildOCITestArtifact(t *testing.T, files map[string]string, layerMediaType string) (*memory.Store, string) {
	t.Helper()
	ctx := context.Background()
	store := memory.New()

	// Build tar.gz of files.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range files {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Typeflag: tar.TypeReg,
			Size:     int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	layerBytes := buf.Bytes()

	layerDesc := ocispec.Descriptor{
		MediaType: layerMediaType,
		Digest:    digest.FromBytes(layerBytes),
		Size:      int64(len(layerBytes)),
	}
	if err := store.Push(ctx, layerDesc, bytes.NewReader(layerBytes)); err != nil {
		t.Fatalf("push layer: %v", err)
	}

	configBytes := []byte("{}")
	configDesc := ocispec.Descriptor{
		MediaType: "application/vnd.kapro.config.v1+json",
		Digest:    digest.FromBytes(configBytes),
		Size:      int64(len(configBytes)),
	}
	if err := store.Push(ctx, configDesc, bytes.NewReader(configBytes)); err != nil {
		t.Fatalf("push config: %v", err)
	}

	manifest := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    []ocispec.Descriptor{layerDesc},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	manifestDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromBytes(manifestBytes),
		Size:      int64(len(manifestBytes)),
	}
	if err := store.Push(ctx, manifestDesc, bytes.NewReader(manifestBytes)); err != nil {
		t.Fatalf("push manifest: %v", err)
	}
	const tag = "v1"
	if err := store.Tag(ctx, manifestDesc, tag); err != nil {
		t.Fatalf("tag: %v", err)
	}
	return store, tag
}

func TestOCIPuller_Pull_RawYAMLArtifact(t *testing.T) {
	store, tag := buildOCITestArtifact(t, map[string]string{
		"a.yaml": "kind: A\napiVersion: v1\n",
		"b.yaml": "kind: B\napiVersion: v1\n",
	}, MediaTypeRawYAML)

	puller := &OCIPuller{
		Resolver: func(ctx context.Context, ref ArtifactRef) (oras.ReadOnlyTarget, string, error) {
			return store, tag, nil
		},
	}
	pa, err := puller.Pull(context.Background(), ArtifactRef{Repository: "irrelevant", Tag: tag})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pa.MediaType != MediaTypeRawYAML {
		t.Fatalf("media type=%s, want %s", pa.MediaType, MediaTypeRawYAML)
	}
	a, err := fs.ReadFile(pa.FS, "a.yaml")
	if err != nil {
		t.Fatalf("read a.yaml: %v", err)
	}
	if string(a) != "kind: A\napiVersion: v1\n" {
		t.Fatalf("a.yaml body = %q", a)
	}
}

func TestOCIPuller_Pull_RejectsMultiLayer(t *testing.T) {
	// Manually build a manifest with two layers and ensure Pull errors out.
	ctx := context.Background()
	store := memory.New()

	pushBlob := func(media string, body []byte) ocispec.Descriptor {
		d := ocispec.Descriptor{
			MediaType: media,
			Digest:    digest.FromBytes(body),
			Size:      int64(len(body)),
		}
		if err := store.Push(ctx, d, bytes.NewReader(body)); err != nil {
			t.Fatalf("push: %v", err)
		}
		return d
	}
	layer1 := pushBlob(MediaTypeRawYAML, []byte("layer1"))
	layer2 := pushBlob(MediaTypeRawYAML, []byte("layer2"))
	config := pushBlob("application/vnd.kapro.config.v1+json", []byte("{}"))
	manifest := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    config,
		Layers:    []ocispec.Descriptor{layer1, layer2},
	}
	mb, _ := json.Marshal(manifest)
	md := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromBytes(mb), Size: int64(len(mb))}
	_ = store.Push(ctx, md, bytes.NewReader(mb))
	_ = store.Tag(ctx, md, "bad")

	puller := &OCIPuller{
		Resolver: func(ctx context.Context, ref ArtifactRef) (oras.ReadOnlyTarget, string, error) {
			return store, "bad", nil
		},
	}
	_, err := puller.Pull(context.Background(), ArtifactRef{Repository: "r", Tag: "bad"})
	if err == nil {
		t.Fatal("expected error for multi-layer artifact")
	}
}

func TestOCIPuller_Pull_EmptyRepositoryRejected(t *testing.T) {
	_, err := (&OCIPuller{}).Pull(context.Background(), ArtifactRef{})
	if err == nil {
		t.Fatal("expected error for empty repo")
	}
}

func TestReadAll_RejectsNegativeSize(t *testing.T) {
	// Hostile manifest claims a negative descriptor size; readAll must
	// refuse to allocate / read rather than panic on make([]byte, 0, neg).
	store := memory.New()
	d := ocispec.Descriptor{
		MediaType: "application/octet-stream",
		Digest:    digest.FromBytes([]byte("x")),
		Size:      -1,
	}
	_, err := readAll(context.Background(), store, d, DefaultMaxLayerBytes)
	if err == nil {
		t.Fatal("expected error for negative size")
	}
}

func TestReadAll_RejectsOversizedDeclaration(t *testing.T) {
	// Hostile manifest claims a descriptor size larger than the cap before
	// any bytes are fetched. Cheaper to reject up-front than to start the
	// transfer and OOM mid-stream.
	store := memory.New()
	d := ocispec.Descriptor{
		MediaType: "application/octet-stream",
		Digest:    digest.FromBytes([]byte("x")),
		Size:      1 << 40, // 1 TiB
	}
	_, err := readAll(context.Background(), store, d, 1024)
	if err == nil {
		t.Fatal("expected error for oversized descriptor")
	}
}

func TestOCIPuller_Pull_RejectsLayerOverCap(t *testing.T) {
	// Build a tar with a single large file; cap the puller at 1 KiB.
	big := bytes.Repeat([]byte("a"), 4096)
	store, tag := buildOCITestArtifact(t, map[string]string{"big.txt": string(big)}, MediaTypeRawYAML)
	puller := &OCIPuller{
		Resolver: func(ctx context.Context, ref ArtifactRef) (oras.ReadOnlyTarget, string, error) {
			return store, tag, nil
		},
		MaxLayerBytes: 1024,
	}
	_, err := puller.Pull(context.Background(), ArtifactRef{Repository: "r", Tag: tag})
	if err == nil {
		t.Fatal("expected error for oversized tar layer")
	}
}

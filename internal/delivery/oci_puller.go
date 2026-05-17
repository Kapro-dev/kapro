package delivery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"testing/fstest"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

// ResolverFactory is the function the puller uses to build an ORAS-side
// registry client. Decoupled so unit tests can supply an in-memory store
// without standing up an HTTPS registry.
//
// On success, the returned target must implement Resolve+Fetch sufficient
// for oras.Copy to mirror the requested reference into the in-memory store
// the puller passes to it.
type ResolverFactory func(ctx context.Context, ref ArtifactRef) (oras.ReadOnlyTarget, string, error)

// OCIPuller is the production Puller: it fetches an OCI artifact via the
// ORAS Go SDK, extracts its single tar+gzip layer, and returns the file
// tree as an fs.FS.
//
// Why a single layer: every spoke-applicable artifact format Kapro supports
// (raw YAML tarball, Helm OCI chart, Kustomize tarball) ships as exactly one
// tar.gz layer per OCI conventions. Multi-layer artifacts are rejected
// loudly — we don't want to silently apply only the first layer.
type OCIPuller struct {
	// Resolver builds the read target for a given ref. Defaults to
	// remoteResolver when nil.
	Resolver ResolverFactory
	// MaxLayerBytes caps the decompressed size of the tar layer to prevent
	// a malicious artifact from exhausting the spoke's memory. Defaults to
	// 64 MiB which is well above any real Helm/Kustomize/raw-YAML payload
	// but small enough that a runaway archive is rejected fast.
	MaxLayerBytes int64
}

// DefaultMaxLayerBytes is the cap applied when OCIPuller.MaxLayerBytes is 0.
const DefaultMaxLayerBytes int64 = 64 * 1024 * 1024

// Pull fetches the artifact, copies it into an in-memory store, walks the
// manifest, and decodes the single tar+gzip layer into an fs.FS.
func (p *OCIPuller) Pull(ctx context.Context, ref ArtifactRef) (*PulledArtifact, error) {
	if ref.Repository == "" {
		return nil, fmt.Errorf("ArtifactRef.Repository required")
	}
	resolver := p.Resolver
	if resolver == nil {
		resolver = remoteResolver
	}
	maxBytes := p.MaxLayerBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxLayerBytes
	}

	source, srcRef, err := resolver(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("build oci resolver for %s: %w", ref.String(), err)
	}

	dst := memory.New()
	desc, err := oras.Copy(ctx, source, srcRef, dst, srcRef, oras.DefaultCopyOptions)
	if err != nil {
		return nil, fmt.Errorf("copy %s: %w", ref.String(), err)
	}

	manifestBytes, err := readAll(ctx, dst, desc, maxBytes)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", ref.String(), err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("decode manifest %s: %w", ref.String(), err)
	}
	if len(manifest.Layers) != 1 {
		return nil, fmt.Errorf("oci artifact %s has %d layers; exactly 1 is required",
			ref.String(), len(manifest.Layers))
	}
	layer := manifest.Layers[0]

	layerBytes, err := readAll(ctx, dst, layer, maxBytes)
	if err != nil {
		return nil, fmt.Errorf("read layer %s of %s: %w", layer.Digest, ref.String(), err)
	}

	mapFS, err := decodeTarGz(layerBytes, maxBytes)
	if err != nil {
		return nil, fmt.Errorf("decode tar.gz layer of %s: %w", ref.String(), err)
	}
	return &PulledArtifact{
		FS:        mapFS,
		Digest:    desc.Digest,
		MediaType: layer.MediaType,
	}, nil
}

// readAll fetches a descriptor's full bytes via Fetch. Bounded by both the
// descriptor's declared Size and maxBytes to keep peak memory predictable
// even when a malicious manifest declares an absurd Size.
//
// A negative declared size is rejected — the OCI spec mandates non-negative
// sizes and any negative value indicates a malformed or hostile descriptor.
// A zero declared size means "unknown" and we fall back to a length-bounded
// ReadAll.
func readAll(ctx context.Context, fetcher oras.ReadOnlyTarget, desc ocispec.Descriptor, maxBytes int64) ([]byte, error) {
	if desc.Size < 0 {
		return nil, fmt.Errorf("descriptor %s declares negative size %d", desc.Digest, desc.Size)
	}
	if desc.Size > maxBytes {
		return nil, fmt.Errorf("descriptor %s declares size %d > cap %d",
			desc.Digest, desc.Size, maxBytes)
	}
	rc, err := fetcher.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	if desc.Size > 0 {
		buf := bytes.NewBuffer(make([]byte, 0, desc.Size))
		if _, err := io.CopyN(buf, rc, desc.Size); err != nil && err != io.EOF {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	return io.ReadAll(io.LimitReader(rc, maxBytes+1))
}

// decodeTarGz reads a single-layer tar+gzip blob into an in-memory fs.FS.
// Returns an error if the decompressed size exceeds maxBytes.
func decodeTarGz(blob []byte, maxBytes int64) (fs.FS, error) {
	gr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil, fmt.Errorf("gzip open: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	out := fstest.MapFS{}
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar read header: %w", err)
		}
		name := path.Clean(hdr.Name)
		if name == "." || name == "" {
			continue
		}
		if strings.HasPrefix(name, "..") || strings.HasPrefix(name, "/") {
			return nil, fmt.Errorf("tar entry has unsafe path %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			out[name] = &fstest.MapFile{Mode: fs.ModeDir | 0o755, ModTime: hdr.ModTime}
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Size < 0 {
				return nil, fmt.Errorf("tar entry %s has negative size", name)
			}
			total += hdr.Size
			if total > maxBytes {
				return nil, fmt.Errorf("tar layer exceeds %d byte cap", maxBytes)
			}
			buf := bytes.NewBuffer(make([]byte, 0, hdr.Size))
			if _, err := io.Copy(buf, io.LimitReader(tr, maxBytes+1)); err != nil {
				return nil, fmt.Errorf("tar read %s: %w", name, err)
			}
			out[name] = &fstest.MapFile{
				Data:    buf.Bytes(),
				Mode:    fs.FileMode(hdr.Mode) & 0o777,
				ModTime: hdr.ModTime,
			}
		default:
			// Skip symlinks, devices, etc. — irrelevant for k8s manifest payloads.
			continue
		}
	}
	return out, nil
}

// remoteResolver builds a remote.Repository client for the given ref,
// wiring credentials per ref.Authn. Used by OCIPuller when no test resolver
// is supplied.
func remoteResolver(ctx context.Context, ref ArtifactRef) (oras.ReadOnlyTarget, string, error) {
	repoRef := ref.Repository
	repo, err := remote.NewRepository(repoRef)
	if err != nil {
		return nil, "", fmt.Errorf("parse repository %s: %w", repoRef, err)
	}
	host := registryHostOf(repoRef)
	switch ref.Authn.Mode {
	case "", AuthAnonymous:
		// default client: no credentials
	case AuthBearer:
		repo.Client = &auth.Client{
			Credential: auth.StaticCredential(host, auth.Credential{
				Username: "oauth2accesstoken",
				Password: ref.Authn.Token,
			}),
		}
	case AuthDockerConfig:
		creds, err := credentialsFromDockerConfig(ref.Authn.DockerConfigPath, host)
		if err != nil {
			return nil, "", err
		}
		repo.Client = &auth.Client{Credential: auth.StaticCredential(host, creds)}
	default:
		return nil, "", fmt.Errorf("unsupported authn mode %q", ref.Authn.Mode)
	}
	srcRef := ref.Tag
	if ref.Digest != "" {
		srcRef = ref.Digest
	}
	if srcRef == "" {
		// remote.Repository can't Resolve "" — fall back to "latest" to keep
		// behaviour predictable.
		srcRef = "latest"
	}
	if _, err := registry.ParseReference(repoRef + ":" + ref.Tag); err != nil && ref.Tag != "" {
		// Sanity: the constructed reference should at least parse.
		return nil, "", fmt.Errorf("invalid reference %s:%s: %w", repoRef, ref.Tag, err)
	}
	return repo, srcRef, nil
}

// registryHostOf extracts the host portion of an OCI repo reference.
// "registry.example.com/path/repo" → "registry.example.com".
func registryHostOf(repoRef string) string {
	if i := strings.Index(repoRef, "/"); i >= 0 {
		return repoRef[:i]
	}
	return repoRef
}

// credentialsFromDockerConfig parses a Docker config.json and returns the
// auth entry matching host (or a wildcard entry). Supports the conventional
// schema with auths[host].{username,password,auth} where auth is base64
// "user:pass".
func credentialsFromDockerConfig(p string, host string) (auth.Credential, error) {
	if p == "" {
		return auth.EmptyCredential, fmt.Errorf("docker-config path is empty")
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return auth.EmptyCredential, fmt.Errorf("read docker-config %s: %w", p, err)
	}
	var doc struct {
		Auths map[string]struct {
			Username string `json:"username,omitempty"`
			Password string `json:"password,omitempty"`
			Auth     string `json:"auth,omitempty"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return auth.EmptyCredential, fmt.Errorf("parse docker-config: %w", err)
	}
	// Find the longest-matching host key (Docker spec allows scheme prefixes).
	keys := make([]string, 0, len(doc.Auths))
	for k := range doc.Auths {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		bare := strings.TrimPrefix(strings.TrimPrefix(k, "https://"), "http://")
		if bare == host || bare == host+"/" || strings.HasPrefix(host, bare) {
			entry := doc.Auths[k]
			if entry.Username != "" || entry.Password != "" {
				return auth.Credential{Username: entry.Username, Password: entry.Password}, nil
			}
			if entry.Auth != "" {
				u, p, err := decodeBasicAuth(entry.Auth)
				if err != nil {
					return auth.EmptyCredential, err
				}
				return auth.Credential{Username: u, Password: p}, nil
			}
		}
	}
	return auth.EmptyCredential, fmt.Errorf("no docker-config entry for host %s in %s", host, p)
}

func decodeBasicAuth(b64 string) (string, string, error) {
	dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", "", fmt.Errorf("decode docker-config auth: %w", err)
	}
	parts := strings.SplitN(string(dec), ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("docker-config auth does not contain user:pass")
	}
	return parts[0], parts[1], nil
}

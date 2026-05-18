package outbound

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"kapro.io/kapro/internal/delivery"
	"kapro.io/kapro/pkg/spokeprovider"
)

func newSpokeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func TestResolve_AnonymousExpansion(t *testing.T) {
	r := &ParametersRefResolver{}
	got, err := r.Resolve(context.Background(), spokeprovider.ReconcileRequest{
		AppKey:         "frontend",
		DesiredVersion: "1.2.3",
		Parameters: map[string]string{
			ParamRepository: "registry.example.com/charts/{appKey}",
			ParamTag:        "v{version}",
		},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := delivery.ArtifactRef{
		Repository: "registry.example.com/charts/frontend",
		Tag:        "v1.2.3",
		Authn:      delivery.Authn{Mode: delivery.AuthAnonymous},
	}
	if got != want {
		t.Fatalf("ref mismatch:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestResolve_DefaultTag(t *testing.T) {
	r := &ParametersRefResolver{}
	got, err := r.Resolve(context.Background(), spokeprovider.ReconcileRequest{
		AppKey:         "api",
		DesiredVersion: "9",
		Parameters:     map[string]string{ParamRepository: "r.io/x"},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Tag != "9" {
		t.Fatalf("default tag template should yield raw version, got %q", got.Tag)
	}
}

func TestResolve_MissingRepositoryFails(t *testing.T) {
	r := &ParametersRefResolver{}
	_, err := r.Resolve(context.Background(), spokeprovider.ReconcileRequest{
		AppKey:         "x",
		DesiredVersion: "1",
		Parameters:     map[string]string{},
	})
	if err == nil || !strings.Contains(err.Error(), "repository") {
		t.Fatalf("expected missing-repository error, got %v", err)
	}
}

func TestResolve_EmptyVersionFails(t *testing.T) {
	r := &ParametersRefResolver{}
	_, err := r.Resolve(context.Background(), spokeprovider.ReconcileRequest{
		AppKey:     "x",
		Parameters: map[string]string{ParamRepository: "r.io/x"},
	})
	if err == nil || !strings.Contains(err.Error(), "desired version") {
		t.Fatalf("expected empty-version error, got %v", err)
	}
}

func TestResolve_UnknownTokenFails(t *testing.T) {
	r := &ParametersRefResolver{}
	_, err := r.Resolve(context.Background(), spokeprovider.ReconcileRequest{
		AppKey:         "a",
		DesiredVersion: "1",
		Parameters: map[string]string{
			ParamRepository: "r.io/x/{nope}",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown template token") {
		t.Fatalf("expected unknown-token error, got %v", err)
	}
}

func TestResolve_BearerFromSecret(t *testing.T) {
	secret := &corev1.Secret{}
	secret.Namespace = "kapro-system"
	secret.Name = "registry-token"
	secret.Data = map[string][]byte{"token": []byte("s3cr3t")}
	c := newSpokeClient(t, secret)

	r := &ParametersRefResolver{Local: c}
	got, err := r.Resolve(context.Background(), spokeprovider.ReconcileRequest{
		AppKey:         "api",
		DesiredVersion: "1.0",
		Parameters: map[string]string{
			ParamRepository:     "r.io/x",
			ParamAuth:           "bearer",
			ParamTokenSecretRef: "kapro-system/registry-token",
		},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Authn.Mode != delivery.AuthBearer || got.Authn.Token != "s3cr3t" {
		t.Fatalf("bad authn: %+v", got.Authn)
	}
}

func TestResolve_BearerMissingSecretFails(t *testing.T) {
	c := newSpokeClient(t)
	r := &ParametersRefResolver{Local: c}
	_, err := r.Resolve(context.Background(), spokeprovider.ReconcileRequest{
		AppKey:         "api",
		DesiredVersion: "1.0",
		Parameters: map[string]string{
			ParamRepository:     "r.io/x",
			ParamAuth:           "bearer",
			ParamTokenSecretRef: "ns/missing",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "read bearer-token Secret") {
		t.Fatalf("expected secret-read error, got %v", err)
	}
}

func TestResolve_BearerRequiresSecretRef(t *testing.T) {
	r := &ParametersRefResolver{Local: newSpokeClient(t)}
	_, err := r.Resolve(context.Background(), spokeprovider.ReconcileRequest{
		AppKey:         "a",
		DesiredVersion: "1",
		Parameters: map[string]string{
			ParamRepository: "r.io/x",
			ParamAuth:       "bearer",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "tokenSecretRef") {
		t.Fatalf("expected tokenSecretRef-required error, got %v", err)
	}
}

func TestResolve_DockerConfig(t *testing.T) {
	r := &ParametersRefResolver{}
	got, err := r.Resolve(context.Background(), spokeprovider.ReconcileRequest{
		AppKey:         "api",
		DesiredVersion: "1.0",
		Parameters: map[string]string{
			ParamRepository:       "r.io/x",
			ParamAuth:             "docker-config",
			ParamDockerConfigPath: "/etc/kapro/dockercfg/config.json",
		},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Authn.Mode != delivery.AuthDockerConfig || got.Authn.DockerConfigPath != "/etc/kapro/dockercfg/config.json" {
		t.Fatalf("bad authn: %+v", got.Authn)
	}
}

func TestResolve_DockerConfigRequiresPath(t *testing.T) {
	r := &ParametersRefResolver{}
	_, err := r.Resolve(context.Background(), spokeprovider.ReconcileRequest{
		AppKey:         "a",
		DesiredVersion: "1",
		Parameters: map[string]string{
			ParamRepository: "r.io/x",
			ParamAuth:       "docker-config",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "dockerConfigPath") {
		t.Fatalf("expected dockerConfigPath-required error, got %v", err)
	}
}

func TestResolve_UnsupportedAuthMode(t *testing.T) {
	r := &ParametersRefResolver{}
	_, err := r.Resolve(context.Background(), spokeprovider.ReconcileRequest{
		AppKey:         "a",
		DesiredVersion: "1",
		Parameters: map[string]string{
			ParamRepository: "r.io/x",
			ParamAuth:       "kerberos",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported-auth error, got %v", err)
	}
}

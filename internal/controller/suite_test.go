package controller_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcfg "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/controller"
	internalgate "kapro.io/kapro/internal/gate"
	"kapro.io/kapro/pkg/actuator"
)

// TestMain ensures envtest binaries are available; if not present we skip integration tests gracefully.
func TestMain(m *testing.M) {
	// Allow unit tests (sync_fsm_test.go) to run without envtest binaries.
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		// Binaries may be directly in bin/k8s or in a versioned subdirectory (e.g. bin/k8s/1.31.0-darwin-arm64).
		base := filepath.Join("..", "..", "bin", "k8s")
		if entries, err := os.ReadDir(base); err == nil {
			for _, e := range entries {
				candidate := filepath.Join(base, e.Name())
				if e.IsDir() {
					if _, err := os.Stat(filepath.Join(candidate, "etcd")); err == nil {
						os.Setenv("KUBEBUILDER_ASSETS", candidate)
						break
					}
				}
			}
			// fallback: binaries directly in base
			if os.Getenv("KUBEBUILDER_ASSETS") == "" {
				if _, err := os.Stat(filepath.Join(base, "etcd")); err == nil {
					os.Setenv("KUBEBUILDER_ASSETS", base)
				}
			}
		}
	}
	os.Exit(m.Run())
}

// setupEnv starts an envtest environment and returns a cancel function.
// It registers ReleaseReconciler FIRST (owns IndexField registrations) then
// SyncReconciler.
//
// Callers must defer the returned cancel func:
//
//	ctx, cancel, c := setupEnv(t)
//	defer cancel()
func setupEnv(t *testing.T) (context.Context, context.CancelFunc, client.Client) {
	t.Helper()

	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set — skipping envtest integration test")
	}

	s := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	env := &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")},
		Scheme:            s,
	}

	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("envtest.Start: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 s,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		// Multiple test envs share the same process — skip global metric name uniqueness check.
		Controller: ctrlcfg.Controller{
			SkipNameValidation: ptr.To(true),
		},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	fakeActuators := actuator.NewRegistry()
	if err := fakeActuators.Register("flux", &fakeActuator{converged: true}); err != nil {
		t.Fatalf("register fake actuator: %v", err)
	}

	recorder := record.NewFakeRecorder(100)

	// IMPORTANT: ReleaseReconciler MUST be registered first — it owns IndexField registrations.
	releaseReconciler := &controller.ReleaseReconciler{
		Client:   mgr.GetClient(),
		Recorder: recorder,
		Scheme:   mgr.GetScheme(),
	}
	if err := releaseReconciler.SetupWithManager(mgr); err != nil {
		t.Fatalf("ReleaseReconciler.SetupWithManager: %v", err)
	}

	syncReconciler := &controller.SyncReconciler{
		Client:           mgr.GetClient(),
		Recorder:         recorder,
		ActuatorRegistry: fakeActuators,
		ApprovalGate:     &internalgate.ApprovalGate{Client: mgr.GetClient()},
	}
	if err := syncReconciler.SetupWithManager(mgr); err != nil {
		t.Fatalf("SyncReconciler.SetupWithManager: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := mgr.Start(ctx); err != nil && ctx.Err() == nil {
			t.Logf("manager exited: %v", err)
		}
	}()

	if !mgr.GetCache().WaitForCacheSync(ctx) {
		cancel()
		t.Fatal("cache did not sync")
	}

	return ctx, cancel, mgr.GetClient()
}

// ---- shared test helpers ----------------------------------------------------

// mustCreate creates obj and registers t.Cleanup to delete it.
func mustCreate(t *testing.T, ctx context.Context, c client.Client, obj client.Object) {
	t.Helper()
	if err := c.Create(ctx, obj); err != nil {
		t.Fatalf("Create %T %s: %v", obj, obj.GetName(), err)
	}
	t.Cleanup(func() {
		_ = c.Delete(context.Background(), obj)
	})
}

// eventually polls fn until it returns true or times out.
func eventually(t *testing.T, fn func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("eventually: timed out waiting for: %s", msg)
}

// eventuallyLong polls fn for up to 90 seconds. Use for E2E chains that must
// pass through multiple requeueNormal (30s) intervals.
func eventuallyLong(t *testing.T, fn func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("eventually: timed out waiting for: %s", msg)
}

// getRelease fetches the latest Release state.
func getRelease(ctx context.Context, c client.Client, key types.NamespacedName) *kaprov1alpha1.Release {
	r := &kaprov1alpha1.Release{}
	_ = c.Get(ctx, key, r)
	return r
}

// ---- fake actuator for tests ------------------------------------------------

type fakeActuator struct {
	applyErr  error
	converged bool
	convErr   error
}

func (f *fakeActuator) Apply(_ context.Context, _ actuator.ApplyRequest) error { return f.applyErr }
func (f *fakeActuator) IsConverged(_ context.Context, _ *kaprov1alpha1.Environment, _, _ string) (bool, error) {
	return f.converged, f.convErr
}
func (f *fakeActuator) Rollback(_ context.Context, _ *kaprov1alpha1.Environment, _ string) error {
	return f.applyErr
}

// ---- shared fixture builders ------------------------------------------------

func makeArtifact(name, ns string) *kaprov1alpha1.Artifact {
	return &kaprov1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: kaprov1alpha1.ArtifactSpec{
			Sources: []kaprov1alpha1.ArtifactSource{
				{
					Type: "oci",
					OCI: &kaprov1alpha1.OCIRef{
						Repository: "172.17.0.1:5000/fleet-bundle",
						Tag:        "v1.2.4",
						Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					},
				},
			},
		},
	}
}

func makeEnvironment(name, ns string, labels map[string]string) *kaprov1alpha1.Environment {
	return &kaprov1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: kaprov1alpha1.EnvironmentSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{Namespace: "flux-system"},
			},
		},
	}
}

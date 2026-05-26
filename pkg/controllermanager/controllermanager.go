// Package controllermanager implements the Kapro controller manager pattern,
// modelled after cloud-controller-manager from k8s.io/cloud-provider.
//
// Every Kapro controller is registered as an InitFunc in the Registry.
// The operator binary iterates the registry and starts only the selected
// controllers — enabling selective deployment without recompilation.
//
// Usage:
//
//	KAPRO_CONTROLLERS=*                          # all canonical controllers
//	KAPRO_CONTROLLERS=*,-trigger                 # all except trigger
//	KAPRO_CONTROLLERS=fleet,promotionrun,cluster # selected core controllers
package controllermanager

import (
	"context"
	"sort"
	"strings"

	"k8s.io/client-go/kubernetes"
	certificatesv1client "k8s.io/client-go/kubernetes/typed/certificates/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	kaproadapter "kapro.io/kapro/pkg/kapro/adapter"
	"kapro.io/kapro/pkg/notification"
	"kapro.io/kapro/pkg/planner"
)

// InitFunc is the initialisation signature every controller must satisfy.
// It mirrors cloud-controller-manager's controller InitFunc:
//
//	enabled bool — false means the controller was intentionally skipped (not an error).
//	err          — hard failure; the manager must abort.
type InitFunc func(ctx context.Context, cc ControllerContext) (enabled bool, err error)

// ControllerContext carries all shared dependencies injected into every
// controller at startup.  Adding a dependency here makes it available to all
// controllers without changing individual InitFunc signatures.
type ControllerContext struct {
	// Manager is the controller-runtime manager. Controllers call
	// SetupWithManager(cc.Manager) inside their InitFunc.
	Manager ctrl.Manager

	// Recorder is the shared event recorder for all controllers.
	Recorder record.EventRecorder

	// ActuatorRegistry resolves KAI implementations by FleetCluster.spec.delivery.
	// Controllers call ActuatorRegistry.Resolve(env.Spec.Substrate.RegistryKey())
	// to get the correct adapter — Flux, Argo, or any registered substrate.
	ActuatorRegistry *actuator.Registry

	// GateRegistry resolves gate names to pkg/gate.Gate implementations.
	// Registry holds BOTH FSM-phase gates (soak, metrics, approval,
	// verification — resolved by fixed name from FSM handlers) AND
	// template-dispatch gates (cel, job, webhook, etc. — resolved by
	// GateTemplate.spec.type). Built-ins are registered by BuildGateRegistry.
	// External gate types register at startup:
	// cc.GateRegistry.Register("my-type", impl). Never nil in production.
	GateRegistry *gate.Registry

	// AdapterRegistry resolves public delivery adapters by substrate kind.
	// SubstrateDiscoveryPolicy uses this for continuous existing-cluster discovery. Promotion
	// execution continues to use ActuatorRegistry until the substrate plugin
	// axis fully replaces the legacy actuator bridge.
	AdapterRegistry *kaproadapter.Registry

	// Planner orders and filters promotion targets. Built-in planner plugins are
	// always present; external KPI plugins can be hot-loaded when the plugin
	// gateway is enabled.
	Planner *planner.Framework

	// Notifier sends promotion lifecycle events to external channels.
	Notifier notification.Notifier

	// ApprovalSecret is the HMAC secret used to sign/verify approval tokens.
	ApprovalSecret []byte

	// ExternalURL is the base URL of this operator (used in approval links).
	ExternalURL string

	// HubAPIURL is the externally-reachable kube-apiserver URL for this hub cluster.
	// Embedded in bootstrap kubeconfigs so spoke clusters can connect.
	// Required in production.
	HubAPIURL string

	// HubCAData is the PEM-encoded CA certificate for the hub kube-apiserver.
	// Embedded in bootstrap kubeconfigs alongside HubAPIURL.
	HubCAData []byte

	// ShardName partitions objects across controller replicas for horizontal scaling.
	// When empty, all objects are processed (backward compatible — no sharding).
	// When set (e.g. "shard-1"), only objects with label kapro.io/shard=<ShardName>
	// are processed (plus unlabeled objects on the default shard).
	// Populated from KAPRO_SHARD env var in cmd/operator/main.go.
	ShardName string
	// ShardIsDefault controls whether this shard processes unlabeled objects.
	// Exactly one active shard should set this when sharding is enabled.
	ShardIsDefault bool

	// HeartbeatNamespace is where spoke controllers renew
	// coordination.k8s.io Lease objects named kapro-heartbeat-<cluster>.
	HeartbeatNamespace string

	// KubeClient is the typed Kubernetes client used by controllers that need
	// operations not exposed by the controller-runtime client — e.g.,
	// ServiceAccounts/token TokenRequest. Non-nil in production.
	KubeClient kubernetes.Interface

	// CertClient is the typed CertificatesV1 client. The bootstrap reconciler
	// needs UpdateApproval which is a subresource verb not routed by
	// controller-runtime Status().Update(). Non-nil in production.
	CertClient certificatesv1client.CertificatesV1Interface

	// PodNamespace is the operator's own namespace. Used by the bootstrap
	// reconciler to place per-cluster bootstrap SAs and kubeconfig Secrets.
	PodNamespace string
}

// Registry maps controller names to their InitFunc.
// Order is not significant — controllers are started concurrently by
// controller-runtime.  Registration is done in controllers.go.
var Registry = map[string]InitFunc{}
var controllerAliases = map[string]string{}

// defaultControllers is the public-preview core controller set. Target is
// intentionally omitted from user-facing defaults and selected implicitly
// whenever promotionrun is enabled.
var defaultControllers = []string{"deliveryunit", "fleet", "plan", "promotion", "promotionrun", "cluster", "substrateclass", "substrate"}

var implicitControllerDependencies = map[string][]string{
	"promotionrun": {"target"},
}

// Register adds an InitFunc to the global Registry.
// Call from init() in controllers.go or from tests.
func Register(name string, fn InitFunc) {
	Registry[name] = fn
}

// RegisterAlias adds a selectable compatibility name for an existing canonical
// controller. Aliases are normalized before startup, so a controller cannot be
// started twice through two names.
func RegisterAlias(alias, canonical string) {
	controllerAliases[alias] = canonical
}

// KnownControllers returns a sorted slice of all registered controller names.
func KnownControllers() []string {
	names := make([]string, 0, len(Registry))
	for name := range Registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DefaultControllerNames returns a copy of the default public-preview core
// controller set.
func DefaultControllerNames() []string {
	names := append([]string(nil), defaultControllers...)
	return names
}

// DefaultControllersFlag returns the comma-separated default controller flag.
func DefaultControllersFlag() string {
	return strings.Join(defaultControllers, ",")
}

func canonicalControllerName(name string) string {
	if canonical, ok := controllerAliases[name]; ok {
		return canonical
	}
	return name
}

// ParseControllerNames resolves a comma-separated --controllers flag value
// into a set of enabled names.
//
//	"*"                 → all canonical controllers
//	"a,b,c"             → only a, b, c
//	"*,-trigger"        → all except trigger
//	"promotionrun"      → promotionrun plus its implicit target controller
func ParseControllerNames(flag string) map[string]bool {
	selected := map[string]bool{}
	tokens := strings.Split(flag, ",")

	// First pass: handle wildcard.
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "*" {
			for name := range Registry {
				selected[name] = true
			}
		}
	}

	// Second pass: explicit inclusions and exclusions.
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" || t == "*" {
			continue
		}
		if strings.HasPrefix(t, "-") {
			delete(selected, canonicalControllerName(strings.TrimPrefix(t, "-")))
		} else {
			selected[canonicalControllerName(t)] = true
		}
	}

	for name, dependencies := range implicitControllerDependencies {
		if !selected[name] {
			continue
		}
		for _, dependency := range dependencies {
			selected[dependency] = true
		}
	}

	return selected
}

// SelectedControllerNames returns known selected controller names in stable
// order, excluding unknown requests.
func SelectedControllerNames(selected map[string]bool) []string {
	var names []string
	for _, name := range KnownControllers() {
		if selected[name] {
			names = append(names, name)
		}
	}
	return names
}

// DisabledControllerNames returns known disabled controller names in stable
// order.
func DisabledControllerNames(selected map[string]bool) []string {
	var names []string
	for _, name := range KnownControllers() {
		if !selected[name] {
			names = append(names, name)
		}
	}
	return names
}

// UnknownControllerNames returns requested names that do not match any
// canonical controller or compatibility alias.
func UnknownControllerNames(selected map[string]bool) []string {
	var names []string
	for name := range selected {
		if _, ok := Registry[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Package controllermanager implements the Kapro controller manager pattern,
// modelled after cloud-controller-manager from k8s.io/cloud-provider.
//
// Every Kapro controller is registered as an InitFunc in the Registry.
// The operator binary iterates the registry and starts only the selected
// controllers — enabling selective deployment without recompilation.
//
// Usage:
//
//	KAPRO_CONTROLLERS=*                  # all (default)
//	KAPRO_CONTROLLERS=*,-releasereport   # all except releasereport
//	KAPRO_CONTROLLERS=release,sync       # only specified controllers
package controllermanager

import (
	"context"
	"strings"

	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/notification"
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

	// ActuatorRegistry resolves KAI implementations by MemberCluster.spec.delivery.
	// Controllers call ActuatorRegistry.Resolve(env.Spec.Delivery.RegistryKey())
	// to get the correct adapter — Flux, Argo, or any registered backend.
	ActuatorRegistry *actuator.Registry

	// GateRegistry resolves gate names to pkg/gate.Gate implementations.
	// Registry holds BOTH FSM-phase gates (soak, metrics, approval,
	// verification — resolved by fixed name from FSM handlers) AND
	// template-dispatch gates (cel, job, webhook, etc. — resolved by
	// GateTemplate.spec.type). Built-ins are registered by BuildGateRegistry.
	// External gate types register at startup:
	// cc.GateRegistry.Register("my-type", impl). Never nil in production.
	GateRegistry *gate.Registry

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

	// HeartbeatNamespace is where spoke controllers renew
	// coordination.k8s.io Lease objects named kapro-heartbeat-<cluster>.
	HeartbeatNamespace string
}

// Registry maps controller names to their InitFunc.
// Order is not significant — controllers are started concurrently by
// controller-runtime.  Registration is done in controllers.go.
var Registry = map[string]InitFunc{}

// Register adds an InitFunc to the global Registry.
// Call from init() in controllers.go or from tests.
func Register(name string, fn InitFunc) {
	Registry[name] = fn
}

// KnownControllers returns a sorted slice of all registered controller names.
func KnownControllers() []string {
	names := make([]string, 0, len(Registry))
	for name := range Registry {
		names = append(names, name)
	}
	return names
}

// ParseControllerNames resolves a comma-separated --controllers flag value
// into a set of enabled names.
//
//	"*"           → all registered controllers
//	"a,b,c"       → only a, b, c
//	"*,-releasetrigger"   → all except releasetrigger
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
			delete(selected, t[1:])
		} else {
			selected[t] = true
		}
	}

	return selected
}

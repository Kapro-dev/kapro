// Package controllermanager implements the Kapro controller manager pattern,
// modelled after cloud-controller-manager from k8s.io/cloud-provider.
//
// Every Kapro controller is registered as an InitFunc in the Registry.
// The operator binary iterates the registry and starts only the selected
// controllers — enabling selective deployment without recompilation.
//
// Usage:
//
//	kapro-operator --controllers=release,promotion,batch,approval
//	kapro-operator --controllers=*                    # all (default)
//	kapro-operator --controllers=*,-kagent            # all except kagent
package controllermanager

import (
	"context"
	"strings"

	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	pkghealth "kapro.io/kapro/pkg/health"
	"kapro.io/kapro/pkg/notification"
	"kapro.io/kapro/pkg/oci"
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

	// ActuatorRegistry resolves per-Environment actuator implementations.
	// Controllers call ActuatorRegistry.Resolve(env.Spec.Actuator.Type) to get
	// the correct Actuator — Flux, KServe, ArgoCD, or a plugin actuator.
	ActuatorRegistry *actuator.Registry

	// Gates — built-in gate implementations.  Any gate field may be nil;
	// each controller documents which gates it requires vs treats as optional.
	Gates GateSet

	// HealthAssessor evaluates workload health in the target namespace.
	HealthAssessor pkghealth.Assessor

	// Notifier sends promotion lifecycle events to external channels.
	Notifier notification.Notifier

	// OCIService enables artifact inspection and promotion operations.
	OCIService oci.Service

	// ApprovalSecret is the HMAC secret used to sign/verify approval tokens.
	ApprovalSecret []byte

	// ExternalURL is the base URL of this operator (used in approval links).
	ExternalURL string
}

// GateSet holds every named gate implementation that can be injected into
// controllers.  Nil values are treated as pass-through by all consumers.
type GateSet struct {
	Soak         gate.Gate
	Metrics      gate.Gate
	Approval     gate.Gate
	Keda         gate.Gate
	MLflow       gate.Gate
	Shadow       gate.Gate
	KGateway     gate.Gate
	Verification gate.Gate
	CEL          gate.Gate
	Argo         gate.Gate
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
//	"*,-kagent"   → all except kagent
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

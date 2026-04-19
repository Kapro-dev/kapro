// Package cel implements the built-in CEL expression gate.
//
// It is the "runc" of Kapro's gate system — always available, no external
// process or vendor dependency required.
//
// The CEL expression is evaluated against a structured activation:
//
//	args         — map[string]string of resolved gate args
//	environment  — kapro Environment object (labels, name, tier, region)
//	sync         — kapro Sync object (version, environmentRef, releaseRef)
//
// Example expression:
//
//	args.error_rate <= "0.01" && environment.labels.wave == "pilot"
package cel

import (
	"context"
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	pkggate "kapro.io/kapro/pkg/gate"
)

// Gate evaluates a CEL expression against sync context.
// Stateless — all state lives in Sync.Status.Gates[].
type Gate struct {
	Client client.Client
}

// Evaluate compiles and evaluates the CEL expression in the GateTemplate.
// Returns Passed=true when the expression evaluates to boolean true.
func (g *Gate) Evaluate(ctx context.Context, req pkggate.Request) (pkggate.Result, error) {
	log := log.FromContext(ctx)

	if req.Template == nil || req.Template.Spec.CEL == nil {
		return pkggate.Result{}, fmt.Errorf("cel gate: template or cel spec is nil")
	}

	expr := req.Template.Spec.CEL.Expression
	if expr == "" {
		return pkggate.Result{}, fmt.Errorf("cel gate: expression is empty")
	}

	// Resolve the Environment object for context variables.
	var envObj kaprov1alpha1.Environment
	if req.Sync != nil && req.Sync.Spec.EnvironmentRef != "" && g.Client != nil {
		if err := g.Client.Get(ctx, client.ObjectKey{Name: req.Sync.Spec.EnvironmentRef}, &envObj); err != nil {
			log.Info("cel gate: could not fetch Environment, proceeding with empty env", "name", req.Sync.Spec.EnvironmentRef)
		}
	}

	activation, err := buildActivation(req.Args, &envObj, req.Sync)
	if err != nil {
		return pkggate.Result{}, fmt.Errorf("cel gate: build activation: %w", err)
	}

	passed, msg, err := evaluate(expr, activation)
	if err != nil {
		return pkggate.Result{
			Phase:   kaprov1alpha1.GatePhaseFailed,
			Message: fmt.Sprintf("CEL evaluation error: %v", err),
		}, nil
	}

	if passed {
		return pkggate.Result{
			Phase:   kaprov1alpha1.GatePhasePassed,
			Message: fmt.Sprintf("CEL expression passed: %s", expr),
		}, nil
	}

	return pkggate.Result{
		Phase:   kaprov1alpha1.GatePhaseFailed,
		Message: fmt.Sprintf("CEL expression failed: %s — %s", expr, msg),
	}, nil
}

// buildActivation constructs the CEL variable map from resolved args + environment + sync.
func buildActivation(args map[string]string, env *kaprov1alpha1.Environment, sync *kaprov1alpha1.Sync) (map[string]any, error) {
	// args: map[string]string — directly accessible as args.key
	argsMap := map[string]any{}
	for k, v := range args {
		argsMap[k] = v
	}

	// environment: flattened fields
	envMap := map[string]any{
		"name":   "",
		"labels": map[string]any{},
	}
	if env != nil && env.Name != "" {
		labelMap := map[string]any{}
		for k, v := range env.Labels {
			labelMap[k] = v
		}
		envMap["name"] = env.Name
		envMap["labels"] = labelMap
	}

	// sync: key fields
	syncMap := map[string]any{
		"name":           "",
		"version":        "",
		"environmentRef": "",
		"releaseRef":     "",
	}
	if sync != nil {
		syncMap["name"] = sync.Name
		syncMap["version"] = sync.Spec.Version
		syncMap["environmentRef"] = sync.Spec.EnvironmentRef
		syncMap["releaseRef"] = sync.Spec.ReleaseRef
	}

	return map[string]any{
		"args":        argsMap,
		"environment": envMap,
		"sync":        syncMap,
	}, nil
}

// evaluate compiles the CEL expression and evaluates it against the activation.
func evaluate(expr string, activation map[string]any) (bool, string, error) {
	env, err := cel.NewEnv(
		cel.Variable("args", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("environment", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("sync", cel.MapType(cel.StringType, cel.StringType)),
	)
	if err != nil {
		return false, "", fmt.Errorf("create CEL env: %w", err)
	}

	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return false, "", fmt.Errorf("compile: %w", issues.Err())
	}

	prg, err := env.Program(ast)
	if err != nil {
		return false, "", fmt.Errorf("program: %w", err)
	}

	out, _, err := prg.Eval(activation)
	if err != nil {
		return false, "", fmt.Errorf("eval: %w", err)
	}

	return isTruthy(out), fmt.Sprintf("result=%v", out), nil
}

func isTruthy(v ref.Val) bool {
	if v == types.True {
		return true
	}
	if b, ok := v.Value().(bool); ok {
		return b
	}
	return false
}

// Ensure Gate implements pkggate.Gate at compile time.
var _ pkggate.Gate = (*Gate)(nil)

// noopObjectReference prevents unused import of corev1 — corev1 is used
// transitively via the pkggate.Result.VendorRef field type.
var _ *corev1.ObjectReference = nil

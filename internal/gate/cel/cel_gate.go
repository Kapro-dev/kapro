// Package cel implements the built-in CEL expression gate.
//
// It is the "runc" of Kapro's gate system — always available, no external
// process or vendor dependency required.
//
// The CEL expression is evaluated against a structured activation:
//
//	args         — map[string]string of resolved gate args
//	target       — kapro MemberCluster object (labels, name)
//	sync         — Kapro delivery context (version, target, releaseRef)
//
// Example expression:
//
//	args.error_rate <= "0.01" && target.labels.wave == "pilot"
package cel

import (
	"context"
	"fmt"
	"sync"

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

var (
	celEnvOnce sync.Once
	celEnv     *cel.Env
	celEnvErr  error

	// celProgramCache is a bounded cache of compiled CEL programs keyed by expression.
	// Max size prevents OOM when many unique expressions are submitted by Release objects.
	celProgramCache    = map[string]cel.Program{}
	celProgramCacheMu  sync.RWMutex
	celProgramCacheMax = 1000
)

// Evaluate compiles and evaluates the CEL expression in the GateTemplate.
// Returns Passed=true when the expression evaluates to boolean true.
func (g *Gate) Evaluate(ctx context.Context, req pkggate.Request) (pkggate.Result, error) {
	log := log.FromContext(ctx)

	if req.Template == nil || req.Template.CEL == nil {
		return pkggate.Result{}, fmt.Errorf("cel gate: template or cel spec is nil")
	}

	expr := req.Template.CEL.Expression
	if expr == "" {
		return pkggate.Result{}, fmt.Errorf("cel gate: expression is empty")
	}

	// Resolve the MemberCluster object for context variables.
	var mcObj kaprov1alpha1.MemberCluster
	if req.Context != nil && req.Context.Target != "" && g.Client != nil {
		if err := g.Client.Get(ctx, client.ObjectKey{Name: req.Context.Target}, &mcObj); err != nil {
			log.Info("cel gate: could not fetch MemberCluster, proceeding with empty cluster context", "name", req.Context.Target)
		}
	}

	activation, err := buildActivation(req.Args, &mcObj, req.Context)
	if err != nil {
		return pkggate.Result{}, fmt.Errorf("cel gate: build activation: %w", err)
	}

	passed, msg, err := evaluate(expr, activation)
	if err != nil {
		return pkggate.Result{
			Phase:   kaprov1alpha1.GatePhaseFailed,
			Message: fmt.Sprintf("CEL evaluation error: %v", err),
			Evidence: []pkggate.Evidence{{
				Type:   "cel",
				Query:  expr,
				Reason: err.Error(),
			}},
		}, nil
	}

	if passed {
		return pkggate.Result{
			Phase:   kaprov1alpha1.GatePhasePassed,
			Message: fmt.Sprintf("CEL expression passed: %s", expr),
			Evidence: []pkggate.Evidence{{
				Type:          "cel",
				Query:         expr,
				ObservedValue: "true",
				Reason:        msg,
			}},
		}, nil
	}

	return pkggate.Result{
		Phase:   kaprov1alpha1.GatePhaseFailed,
		Message: fmt.Sprintf("CEL expression failed: %s — %s", expr, msg),
		Evidence: []pkggate.Evidence{{
			Type:          "cel",
			Query:         expr,
			ObservedValue: "false",
			Reason:        msg,
		}},
	}, nil
}

// buildActivation constructs the CEL variable map from resolved args + cluster + gate context.
func buildActivation(args map[string]string, mc *kaprov1alpha1.MemberCluster, gateCtx *pkggate.Context) (map[string]any, error) {
	// args: map[string]string — directly accessible as args.key
	argsMap := map[string]any{}
	for k, v := range args {
		argsMap[k] = v
	}

	// target: flattened fields
	targetMap := map[string]any{
		"name":   "",
		"labels": map[string]any{},
	}
	if mc != nil && mc.Name != "" {
		labelMap := map[string]any{}
		for k, v := range mc.Labels {
			labelMap[k] = v
		}
		targetMap["name"] = mc.Name
		targetMap["labels"] = labelMap
	}

	// sync: key fields
	syncMap := map[string]any{
		"name":       "",
		"version":    "",
		"target":     "",
		"releaseRef": "",
	}
	if gateCtx != nil {
		syncMap["name"] = gateCtx.Name
		syncMap["version"] = gateCtx.Version
		syncMap["target"] = gateCtx.Target
		syncMap["releaseRef"] = gateCtx.ReleaseRef
	}

	return map[string]any{
		"args":   argsMap,
		"target": targetMap,
		"sync":   syncMap,
	}, nil
}

// evaluate compiles the CEL expression and evaluates it against the activation.
func evaluate(expr string, activation map[string]any) (bool, string, error) {
	env, err := sharedCELEnv()
	if err != nil {
		return false, "", err
	}

	prg, err := cachedCELProgram(env, expr)
	if err != nil {
		return false, "", err
	}

	out, _, err := prg.Eval(activation)
	if err != nil {
		return false, "", fmt.Errorf("eval: %w", err)
	}

	return isTruthy(out), fmt.Sprintf("result=%v", out), nil
}

func sharedCELEnv() (*cel.Env, error) {
	celEnvOnce.Do(func() {
		celEnv, celEnvErr = cel.NewEnv(
			cel.Variable("args", cel.MapType(cel.StringType, cel.StringType)),
			cel.Variable("target", cel.MapType(cel.StringType, cel.DynType)),
			cel.Variable("sync", cel.MapType(cel.StringType, cel.StringType)),
		)
	})
	if celEnvErr != nil {
		return nil, fmt.Errorf("create CEL env: %w", celEnvErr)
	}
	return celEnv, nil
}

func cachedCELProgram(env *cel.Env, expr string) (cel.Program, error) {
	celProgramCacheMu.RLock()
	prg, ok := celProgramCache[expr]
	celProgramCacheMu.RUnlock()
	if ok {
		return prg, nil
	}

	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("compile: %w", issues.Err())
	}

	prg, err := env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("program: %w", err)
	}

	// Only cache if below the size limit; if at limit, return the compiled
	// program for this call without storing it to prevent unbounded growth.
	celProgramCacheMu.Lock()
	if len(celProgramCache) < celProgramCacheMax {
		celProgramCache[expr] = prg
	}
	celProgramCacheMu.Unlock()
	return prg, nil
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

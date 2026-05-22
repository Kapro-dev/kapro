package admission

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

const maxGateExpressionOperands = 128

// GateExpressionValidator validates GateExpression objects on CREATE and UPDATE.
type GateExpressionValidator struct {
	decoder admission.Decoder
	reader  client.Reader
}

// NewGateExpressionValidator returns a configured GateExpressionValidator.
func NewGateExpressionValidator(decoder admission.Decoder, reader client.Reader) *GateExpressionValidator {
	return &GateExpressionValidator{decoder: decoder, reader: reader}
}

// Handle implements admission.Handler.
func (v *GateExpressionValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	var expr kaprov1alpha2.GateExpression
	if err := v.decoder.DecodeRaw(req.Object, &expr); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	var oldExpr *kaprov1alpha2.GateExpression
	if req.Operation == admissionv1.Update && len(req.OldObject.Raw) > 0 {
		var old kaprov1alpha2.GateExpression
		if err := v.decoder.DecodeRaw(req.OldObject, &old); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		oldExpr = &old
	}
	if err := validateGateExpression(ctx, v.reader, &expr, oldExpr); err != nil {
		return admission.Denied(err.Error())
	}
	if req.Operation == admissionv1.Create {
		if fe := requireTeamLabel(expr.Labels); fe != nil {
			return admission.Denied(fe.Error())
		}
	}
	return admission.Allowed("")
}

func validateGateExpression(ctx context.Context, reader client.Reader, expr *kaprov1alpha2.GateExpression, oldExpr *kaprov1alpha2.GateExpression) error {
	if len(expr.Spec.Operands) == 0 {
		return fmt.Errorf("spec.operands must contain at least one operand")
	}
	if len(expr.Spec.Operands) > maxGateExpressionOperands {
		return fmt.Errorf("spec.operands must contain at most %d operands", maxGateExpressionOperands)
	}
	if err := validateGateExpressionOperator(expr); err != nil {
		return err
	}
	for i, operand := range expr.Spec.Operands {
		inline := operand.InlineGate != nil
		ref := strings.TrimSpace(operand.ExpressionRef) != ""
		switch {
		case inline == ref:
			return fmt.Errorf("spec.operands[%d]: exactly one of inlineGate or expressionRef must be set", i)
		case ref && operand.ExpressionRef != strings.TrimSpace(operand.ExpressionRef):
			return fmt.Errorf("spec.operands[%d].expressionRef must not contain surrounding whitespace", i)
		case inline && operand.InlineGate.ExpressionRef != "":
			return fmt.Errorf("spec.operands[%d].inlineGate.expressionRef must not be set; use operand.expressionRef", i)
		}
	}
	if reader == nil || expr.Name == "" {
		return nil
	}
	if cycle, delayPath, err := gateExpressionReferenceAnalysis(ctx, reader, expr); err != nil {
		return err
	} else if cycle != "" {
		return fmt.Errorf("spec.operands.expressionRef cycle detected: %s", cycle)
	} else if delayPath != "" {
		return fmt.Errorf("spec.operands.expressionRef cannot reference DELAY GateExpression: %s", delayPath)
	}
	if expr.Spec.Operator == "DELAY" && (oldExpr == nil || oldExpr.Spec.Operator != "DELAY") {
		if parent, err := existingGateExpressionParent(ctx, reader, expr.Name); err != nil {
			return err
		} else if parent != "" {
			return fmt.Errorf("cannot make GateExpression %q a DELAY because it is referenced by %q", expr.Name, parent)
		}
	}
	return nil
}

func validateGateExpressionOperator(expr *kaprov1alpha2.GateExpression) error {
	switch expr.Spec.Operator {
	case "ALL", "ANY":
		if len(expr.Spec.Weights) > 0 {
			return fmt.Errorf("operator %s does not accept spec.weights", expr.Spec.Operator)
		}
		if expr.Spec.Threshold != nil {
			return fmt.Errorf("operator %s does not accept spec.threshold", expr.Spec.Operator)
		}
	case "NOT":
		if len(expr.Spec.Operands) != 1 {
			return fmt.Errorf("operator NOT requires exactly one operand")
		}
		if len(expr.Spec.Weights) > 0 {
			return fmt.Errorf("operator NOT does not accept spec.weights")
		}
		if expr.Spec.Threshold != nil {
			return fmt.Errorf("operator NOT does not accept spec.threshold")
		}
	case "WEIGHTED_SUM":
		if len(expr.Spec.Weights) != len(expr.Spec.Operands) {
			return fmt.Errorf("operator WEIGHTED_SUM requires len(weights) == len(operands)")
		}
		if expr.Spec.Threshold == nil || *expr.Spec.Threshold <= 0 {
			return fmt.Errorf("operator WEIGHTED_SUM requires threshold > 0")
		}
		sum := int64(0)
		for i, weight := range expr.Spec.Weights {
			if weight < 0 {
				return fmt.Errorf("spec.weights[%d] must be non-negative", i)
			}
			sum += int64(weight)
			if sum > math.MaxInt32 {
				return fmt.Errorf("operator WEIGHTED_SUM total weight must be <= %d", math.MaxInt32)
			}
		}
		// Controller pass condition is strict (passedSum > threshold),
		// so threshold >= total weight is unsatisfiable. Reject these
		// at admission instead of admitting expressions that can only
		// ever Fail.
		if int64(*expr.Spec.Threshold) >= sum {
			return fmt.Errorf("operator WEIGHTED_SUM requires threshold < sum(weights); got threshold=%d sum=%d", *expr.Spec.Threshold, sum)
		}
	case "THRESHOLD":
		if expr.Spec.Threshold == nil || *expr.Spec.Threshold <= 0 || int(*expr.Spec.Threshold) > len(expr.Spec.Operands) {
			return fmt.Errorf("operator THRESHOLD requires 0 < threshold <= len(operands)")
		}
		if len(expr.Spec.Weights) > 0 {
			return fmt.Errorf("operator THRESHOLD does not accept spec.weights")
		}
	case "DELAY":
		if len(expr.Spec.Operands) != 1 {
			return fmt.Errorf("operator DELAY requires exactly one operand")
		}
		if len(expr.Spec.Weights) > 0 {
			return fmt.Errorf("operator DELAY does not accept spec.weights")
		}
		if expr.Spec.Threshold != nil {
			return fmt.Errorf("operator DELAY does not accept spec.threshold")
		}
		duration, err := time.ParseDuration(strings.TrimSpace(expr.Spec.Parameters["duration"]))
		if err != nil {
			return fmt.Errorf("operator DELAY requires parameters.duration as a Go duration: %w", err)
		}
		if duration <= 0 {
			return fmt.Errorf("operator DELAY requires parameters.duration > 0")
		}
	default:
		return fmt.Errorf("unsupported operator %s", expr.Spec.Operator)
	}
	return nil
}

func gateExpressionReferenceAnalysis(ctx context.Context, reader client.Reader, root *kaprov1alpha2.GateExpression) (cyclePath, delayPath string, err error) {
	const (
		unvisited = 0
		inStack   = 1
		visited   = 2
	)
	state := map[string]int{}
	path := []string{}
	cache := map[string]*kaprov1alpha2.GateExpression{root.Name: root}

	load := func(name string) (*kaprov1alpha2.GateExpression, error) {
		if expr, ok := cache[name]; ok {
			return expr, nil
		}
		var expr kaprov1alpha2.GateExpression
		if err := reader.Get(ctx, types.NamespacedName{Name: name}, &expr); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("spec.operands.expressionRef: unknown GateExpression %q", name)
			}
			return nil, fmt.Errorf("get GateExpression %q: %w", name, err)
		}
		cache[name] = &expr
		return &expr, nil
	}

	var dfs func(string) (string, string, error)
	dfs = func(name string) (string, string, error) {
		state[name] = inStack
		path = append(path, name)
		expr, err := load(name)
		if err != nil {
			return "", "", err
		}
		for _, dep := range gateExpressionDependencyNames(expr.Spec.Operands) {
			switch state[dep] {
			case inStack:
				cycle := append(append([]string{}, path...), dep)
				return strings.Join(cycle, "→"), "", nil
			case unvisited:
				child, err := load(dep)
				if err != nil {
					return "", "", err
				}
				if child.Spec.Operator == "DELAY" {
					delay := append(append([]string{}, path...), dep)
					return "", strings.Join(delay, "→"), nil
				}
				if cycle, delay, err := dfs(dep); cycle != "" || delay != "" || err != nil {
					return cycle, delay, err
				}
			}
		}
		path = path[:len(path)-1]
		state[name] = visited
		return "", "", nil
	}
	return dfs(root.Name)
}

func existingGateExpressionParent(ctx context.Context, reader client.Reader, childName string) (string, error) {
	var list kaprov1alpha2.GateExpressionList
	if err := reader.List(ctx, &list); err != nil {
		return "", fmt.Errorf("list GateExpressions referencing %q: %w", childName, err)
	}
	for _, candidate := range list.Items {
		if candidate.Name == childName {
			continue
		}
		for _, operand := range candidate.Spec.Operands {
			if operand.ExpressionRef == childName {
				return candidate.Name, nil
			}
		}
	}
	return "", nil
}

func gateExpressionDependencyNames(operands []kaprov1alpha2.GateExpressionOperand) []string {
	var names []string
	for _, operand := range operands {
		if operand.ExpressionRef != "" {
			names = append(names, operand.ExpressionRef)
		}
	}
	return names
}

// ValidateGateExpression is an exported test helper for validation that does
// not require API lookup.
func ValidateGateExpression(expr *kaprov1alpha2.GateExpression) error {
	return validateGateExpression(context.Background(), nil, expr, nil)
}

// ValidateGateExpressionWithReader is an exported test helper for reference
// and cycle validation.
func ValidateGateExpressionWithReader(ctx context.Context, reader client.Reader, expr *kaprov1alpha2.GateExpression) error {
	return validateGateExpression(ctx, reader, expr, nil)
}

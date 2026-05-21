package admission

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// GateExpressionValidator validates GateExpression objects on CREATE and
// UPDATE. v0.1.2 admits only the ALL operator and rejects reference cycles.
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
	if err := validateGateExpression(ctx, v.reader, &expr); err != nil {
		return admission.Denied(err.Error())
	}
	if req.Operation == admissionv1.Create {
		if fe := requireTeamLabel(expr.Labels); fe != nil {
			return admission.Denied(fe.Error())
		}
	}
	return admission.Allowed("")
}

func validateGateExpression(ctx context.Context, reader client.Reader, expr *kaprov1alpha2.GateExpression) error {
	if expr.Spec.Operator != "ALL" {
		return fmt.Errorf("operator %s is reserved for v0.2.0; use ALL", expr.Spec.Operator)
	}
	if len(expr.Spec.Weights) > 0 || expr.Spec.Threshold != nil {
		return fmt.Errorf("spec.weights and spec.threshold are reserved for v0.2.0")
	}
	if len(expr.Spec.Operands) == 0 {
		return fmt.Errorf("spec.operands must contain at least one operand")
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
	if cycle, err := gateExpressionCycle(ctx, reader, expr); err != nil {
		return err
	} else if cycle != "" {
		return fmt.Errorf("spec.operands.expressionRef cycle detected: %s", cycle)
	}
	return nil
}

func gateExpressionCycle(ctx context.Context, reader client.Reader, root *kaprov1alpha2.GateExpression) (string, error) {
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

	var dfs func(string) (string, error)
	dfs = func(name string) (string, error) {
		state[name] = inStack
		path = append(path, name)
		expr, err := load(name)
		if err != nil {
			return "", err
		}
		for _, dep := range gateExpressionDependencyNames(expr.Spec.Operands) {
			switch state[dep] {
			case inStack:
				cycle := append(append([]string{}, path...), dep)
				return strings.Join(cycle, "→"), nil
			case unvisited:
				if result, err := dfs(dep); result != "" || err != nil {
					return result, err
				}
			}
		}
		path = path[:len(path)-1]
		state[name] = visited
		return "", nil
	}
	return dfs(root.Name)
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
	return validateGateExpression(context.Background(), nil, expr)
}

// ValidateGateExpressionWithReader is an exported test helper for reference
// and cycle validation.
func ValidateGateExpressionWithReader(ctx context.Context, reader client.Reader, expr *kaprov1alpha2.GateExpression) error {
	return validateGateExpression(ctx, reader, expr)
}

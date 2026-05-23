// Package gate is the legacy compatibility import path. New code should import
// kapro.io/kapro/pkg/kapro/gate.
package gate

import kgate "kapro.io/kapro/pkg/kapro/gate"

type (
	Predicate       = kgate.Predicate
	PredicateFunc   = kgate.PredicateFunc
	Gate            = kgate.Gate
	Func            = kgate.Func
	Request         = kgate.Request
	Result          = kgate.Result
	Phase           = kgate.Phase
	Context         = kgate.Context
	Registry        = kgate.Registry
	ConditionResult = kgate.ConditionResult
	Evidence        = kgate.Evidence
	Projection      = kgate.Projection
)

const (
	PhasePassed       = kgate.PhasePassed
	PhaseFailed       = kgate.PhaseFailed
	PhaseInconclusive = kgate.PhaseInconclusive

	Passed       = kgate.Passed
	Failed       = kgate.Failed
	Inconclusive = kgate.Inconclusive
)

var (
	NewRegistry               = kgate.NewRegistry
	NewRegistryWithoutTracing = kgate.NewRegistryWithoutTracing
	MakePassed                = kgate.MakePassed
	MakeFailed                = kgate.MakeFailed
	MakeInconclusive          = kgate.MakeInconclusive
	Recover                   = kgate.Recover
	WithTracing               = kgate.WithTracing
)

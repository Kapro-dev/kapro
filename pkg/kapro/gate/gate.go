// Package gate is the SDK-facing import path for programmable Kapro gates.
package gate

import pkggate "kapro.io/kapro/pkg/gate"

type (
	Gate            = pkggate.Gate
	Func            = pkggate.Func
	Request         = pkggate.Request
	Result          = pkggate.Result
	Phase           = pkggate.Phase
	Registry        = pkggate.Registry
	Context         = pkggate.Context
	ConditionResult = pkggate.ConditionResult
	Evidence        = pkggate.Evidence
	Projection      = pkggate.Projection
)

const (
	Passed  = pkggate.Passed
	Failed  = pkggate.Failed
	Pending = pkggate.Pending
)

var (
	NewRegistry = pkggate.NewRegistry
	MakePassed  = pkggate.MakePassed
	MakeFailed  = pkggate.MakeFailed
	MakePending = pkggate.MakePending
	Recover     = pkggate.Recover
)

// Package gate re-exports KGI from the public pkg layer.
// All new code should import kapro.io/kapro/pkg/gate directly.
package gate

import pkggate "kapro.io/kapro/pkg/gate"

type (
	Context    = pkggate.Context
	Evidence   = pkggate.Evidence
	Gate       = pkggate.Gate
	Projection = pkggate.Projection
	Request    = pkggate.Request
	Result     = pkggate.Result
)

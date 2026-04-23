// Package gate re-exports KGI from the public pkg layer.
// All new code should import kapro.io/kapro/pkg/gate directly.
package gate

import pkggate "kapro.io/kapro/pkg/gate"

type (
Context = pkggate.Context
Gate    = pkggate.Gate
Request = pkggate.Request
Result  = pkggate.Result
)

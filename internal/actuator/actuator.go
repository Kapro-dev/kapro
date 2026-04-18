// Package actuator re-exports KAI from the public pkg layer.
// All new code should import kapro.io/kapro/pkg/actuator directly.
// This file is preserved for backward compatibility within the module.
package actuator

import pkgactuator "kapro.io/kapro/pkg/actuator"

// Re-export KAI types so existing internal packages compile without changes.
type (
Actuator    = pkgactuator.Actuator
ApplyRequest = pkgactuator.ApplyRequest
)

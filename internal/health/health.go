// Package health re-exports KHI from the public pkg layer.
// All new code should import kapro.io/kapro/pkg/health directly.
package health

import pkghealth "kapro.io/kapro/pkg/health"

type (
Assessor       = pkghealth.Assessor
AssessRequest  = pkghealth.AssessRequest
AssessResult   = pkghealth.AssessResult
ResourceHealth = pkghealth.ResourceHealth
Status         = pkghealth.Status
)

const (
StatusHealthy     = pkghealth.StatusHealthy
StatusProgressing = pkghealth.StatusProgressing
StatusDegraded    = pkghealth.StatusDegraded
StatusSuspended   = pkghealth.StatusSuspended
StatusMissing     = pkghealth.StatusMissing
StatusUnknown     = pkghealth.StatusUnknown
)

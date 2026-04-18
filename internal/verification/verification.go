// Package verification re-exports KVI from the public pkg layer.
// All new code should import kapro.io/kapro/pkg/verification directly.
package verification

import pkgverification "kapro.io/kapro/pkg/verification"

type (
Verifier          = pkgverification.Verifier
VerifyRequest     = pkgverification.VerifyRequest
VerifyResult      = pkgverification.VerifyResult
KeylessConfig     = pkgverification.KeylessConfig
KeyConfig         = pkgverification.KeyConfig
AttestationConfig = pkgverification.AttestationConfig
NopVerifier       = pkgverification.NopVerifier
)

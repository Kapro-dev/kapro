// Package oci re-exports KRI from the public pkg layer.
// All new code should import kapro.io/kapro/pkg/oci directly.
package oci

import pkgoci "kapro.io/kapro/pkg/oci"

type (
Service      = pkgoci.Service
ArtifactInfo = pkgoci.ArtifactInfo
AuthConfig   = pkgoci.AuthConfig
)

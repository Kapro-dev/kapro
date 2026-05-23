// Package actuator is a compatibility bridge for the public SDK package
// kapro.io/kapro/pkg/kapro/actuator.
//
// New code should import kapro.io/kapro/pkg/kapro/actuator. These aliases keep
// existing v0.2.x consumers and built-in runtime actuators source-compatible.
package actuator

import kaproactuator "kapro.io/kapro/pkg/kapro/actuator"

const ContractVersionV1Alpha1 = kaproactuator.ContractVersionV1Alpha1

type ApplyRequest = kaproactuator.ApplyRequest
type DeltaApplyRequest = kaproactuator.DeltaApplyRequest
type Actuator = kaproactuator.Actuator
type BackendObjectReporter = kaproactuator.BackendObjectReporter
type Capabilities = kaproactuator.Capabilities
type Substrate = kaproactuator.Substrate

var WithTracing = kaproactuator.WithTracing

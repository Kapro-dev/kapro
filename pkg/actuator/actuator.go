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
type StageRequest = kaproactuator.StageRequest
type StageHandle = kaproactuator.StageHandle
type CommitResult = kaproactuator.CommitResult
type Actuator = kaproactuator.Actuator
type TwoPhaseStaging = kaproactuator.TwoPhaseStaging
type SubstrateObjectReporter = kaproactuator.SubstrateObjectReporter
type Capabilities = kaproactuator.Capabilities
type Substrate = kaproactuator.Substrate

var (
	ErrTwoPhaseUnsupported = kaproactuator.ErrTwoPhaseUnsupported
	AsTwoPhase             = kaproactuator.AsTwoPhase
	WithTracing            = kaproactuator.WithTracing
)

// Package bridge provides KSI bridge implementations that proxy calls to
// external Kapro plugins over gRPC.
//
// Wire format: JSON over gRPC (proto-JSON compatible). Method paths follow the
// proto service definitions in proto/kapro/v1alpha1/.
//
// When proto codegen is added (buf generate), replace the json.Marshal/Unmarshal
// calls with proto.Marshal/proto.Unmarshal using the generated types.
// The method paths and semantics are identical.
package bridge

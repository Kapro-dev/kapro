package bridge

import (
	"encoding/json"
	"fmt"

	"google.golang.org/grpc/encoding"
)

// jsonCodecName is the content-subtype registered with gRPC.
// Use grpc.ForceCodec(bridge.JSONCodec{}) on a connection to activate it.
const jsonCodecName = "json"

// JSONCodec implements encoding.Codec using standard JSON.
// It handles the rawMessage wrapper transparently:
//   - Send: rawMessage.data is []byte — sent as-is
//   - Recv: rawMessage.data is *[]byte — raw bytes copied in
//
// For all other types, standard json.Marshal/Unmarshal is used.
type JSONCodec struct{}

func (JSONCodec) Name() string { return jsonCodecName }

func (JSONCodec) Marshal(v interface{}) ([]byte, error) {
	if rm, ok := v.(*rawMessage); ok {
		if b, ok2 := rm.data.([]byte); ok2 {
			return b, nil
		}
		return nil, fmt.Errorf("jsonCodec: rawMessage.data must be []byte for send, got %T", rm.data)
	}
	return json.Marshal(v)
}

func (JSONCodec) Unmarshal(data []byte, v interface{}) error {
	if rm, ok := v.(*rawMessage); ok {
		if ptr, ok2 := rm.data.(*[]byte); ok2 {
			*ptr = make([]byte, len(data))
			copy(*ptr, data)
			return nil
		}
		return fmt.Errorf("jsonCodec: rawMessage.data must be *[]byte for recv, got %T", rm.data)
	}
	return json.Unmarshal(data, v)
}

func init() {
	// Register the JSON codec globally so any grpc.Dial that uses
	// grpc.ForceCodec(bridge.JSONCodec{}) or grpc.CallContentSubtype("json")
	// picks it up automatically.
	encoding.RegisterCodec(JSONCodec{})
}

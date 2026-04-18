// Package keda implements a Gate that evaluates promotion readiness by querying
// a KEDA-compatible external scaler gRPC server.
//
// Any service implementing the KEDA external scaler protocol becomes a free
// promotion gate: Kafka consumer lag, Redis list length, SQS depth, Prometheus
// queries, RabbitMQ, Datadog, and 55+ more KEDA scalers.
//
// Configuration — two ways to supply it:
//
//  1. JSON blob in MetricGate.Config:
//     {
//     "endpoint":    "grpc://keda-metrics-adapter.keda.svc.cluster.local:9090",
//     "scaler_id":   "kafka-consumer-lag",
//     "namespace":   "flux-p527-workloads",
//     "metric_name": "kafkaConsumerLag",
//     "threshold":   100,
//     "operator":    "lte"
//     }
//
//  2. MetricGate struct fields (convenience):
//     provider:  keda
//     endpoint:  grpc://...
//     query:     <metric_name>
//     threshold: 100
//
// operator "lte": passes when metric_value <= threshold (lag is low enough)
// operator "gte": passes when metric_value >= threshold (queue has enough messages)
// operator "eq":  passes when metric_value == threshold
package keda

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"kapro.io/kapro/pkg/gate"
)

// Config is the JSON-encoded gate configuration stored in MetricGate.Config.
type Config struct {
	// Endpoint is the gRPC address of the KEDA external scaler server.
	// Supports grpc:// and grpcs:// schemes.
	Endpoint string `json:"endpoint"`

	// ScalerID identifies which scaler to query — passed as ScaledObjectRef.Name.
	ScalerID string `json:"scaler_id"`

	// Namespace of the ScaledObject being monitored.
	Namespace string `json:"namespace"`

	// MetricName is the KEDA metric name to retrieve from GetMetrics.
	MetricName string `json:"metric_name"`

	// Threshold is the value to compare the metric against.
	Threshold float64 `json:"threshold"`

	// Operator controls the comparison: lte | gte | eq. Default: lte.
	Operator string `json:"operator"`
}

// Validate checks that all required fields are set.
func (c *Config) Validate() error {
	if c.Endpoint == "" {
		return fmt.Errorf("keda gate: endpoint is required")
	}
	if c.MetricName == "" {
		return fmt.Errorf("keda gate: metric_name is required")
	}
	switch c.Operator {
	case "", "lte", "gte", "eq":
		// valid
	default:
		return fmt.Errorf("keda gate: operator must be lte | gte | eq, got %q", c.Operator)
	}
	return nil
}

func (c *Config) operator() string {
	if c.Operator == "" {
		return "lte"
	}
	return c.Operator
}

// Gate implements gate.Gate by querying a KEDA external scaler gRPC server.
type Gate struct{}

// Evaluate connects to the KEDA external scaler endpoint, queries the metric,
// and returns pass/fail based on the configured threshold and operator.
func (g *Gate) Evaluate(ctx context.Context, req gate.Request) (gate.Result, error) {
	logger := log.FromContext(ctx)

	if req.Policy == nil {
		return gate.Result{Passed: true, Message: "no policy — skipping KEDA gate"}, nil
	}
	if req.MetricIndex >= len(req.Policy.Spec.Gate.Metrics) {
		return gate.Result{}, fmt.Errorf("keda gate: metric index %d out of range", req.MetricIndex)
	}

	metric := req.Policy.Spec.Gate.Metrics[req.MetricIndex]

	var cfg Config
	if len(metric.Config) > 0 {
		if err := json.Unmarshal(metric.Config, &cfg); err != nil {
			return gate.Result{}, fmt.Errorf("keda gate: parse config: %w", err)
		}
	} else {
		// Fall back to the MetricGate struct fields.
		cfg = Config{
			Endpoint:   metric.Endpoint,
			MetricName: metric.Query,
			Threshold:  metric.Threshold,
			Operator:   "lte",
		}
	}

	if err := cfg.Validate(); err != nil {
		return gate.Result{}, err
	}

	value, err := queryMetric(ctx, cfg)
	if err != nil {
		logger.Error(err, "KEDA gate: failed to query metric", "endpoint", cfg.Endpoint)
		return gate.Result{
			Passed:     false,
			Message:    fmt.Sprintf("KEDA query failed: %v", err),
			RetryAfter: "30s",
		}, nil
	}

	passed := compare(value, cfg.Threshold, cfg.operator())
	msg := fmt.Sprintf("KEDA metric %q = %.2f, threshold = %.2f (%s): %s",
		cfg.MetricName, value, cfg.Threshold, cfg.operator(), passStr(passed))

	logger.Info("KEDA gate evaluated",
		"metric", cfg.MetricName,
		"value", value,
		"threshold", cfg.Threshold,
		"operator", cfg.operator(),
		"passed", passed,
	)

	result := gate.Result{Passed: passed, Message: msg}
	if !passed {
		result.RetryAfter = "30s"
	}
	return result, nil
}

// queryMetric dials the KEDA external scaler gRPC server and retrieves the
// current metric value using the KEDA ExternalScaler.GetMetrics RPC.
//
// We encode/decode protobuf manually to avoid importing KEDA as a module
// dependency — the wire format for the two relevant messages is trivial.
func queryMetric(ctx context.Context, cfg Config) (float64, error) {
	endpoint := stripScheme(cfg.Endpoint)

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return 0, fmt.Errorf("dial %s: %w", endpoint, err)
	}
	defer conn.Close()

	reqBytes := encodeGetMetricsRequest(cfg.ScalerID, cfg.Namespace, cfg.MetricName)

	var respBytes []byte
	callCtx, callCancel := context.WithTimeout(dialCtx, 15*time.Second)
	defer callCancel()

	resp := &rawMessage{recv: &respBytes}
	err = conn.Invoke(callCtx,
		"/externalscaler.ExternalScaler/GetMetrics",
		&rawMessage{data: reqBytes},
		resp,
	)
	if err != nil {
		return 0, fmt.Errorf("GetMetrics RPC: %w", err)
	}

	value, err := decodeMetricValue(respBytes, cfg.MetricName)
	if err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}
	return value, nil
}

// rawMessage is a grpc codec-compatible wrapper for hand-encoded proto bytes.
// It implements both the send (Marshal) and receive (Unmarshal) sides.
type rawMessage struct {
	data []byte
	recv *[]byte
}

func (m *rawMessage) ProtoSize() int           { return len(m.data) }
func (m *rawMessage) Marshal() ([]byte, error) { return m.data, nil }
func (m *rawMessage) Reset()                   {}
func (m *rawMessage) String() string           { return fmt.Sprintf("raw(%d bytes)", len(m.data)) }
func (m *rawMessage) ProtoMessage()            {}

// Unmarshal captures the raw bytes returned by the server for manual decoding.
func (m *rawMessage) Unmarshal(b []byte) error {
	if m.recv != nil {
		*m.recv = append([]byte(nil), b...)
	}
	return nil
}

// encodeGetMetricsRequest manually encodes a KEDA GetMetricsRequest.
//
//	message GetMetricsRequest {
//	  ScaledObjectRef scaledObjectRef = 1;
//	  string metricName = 2;
//	}
//	message ScaledObjectRef {
//	  string name = 1;
//	  string namespace = 2;
//	}
func encodeGetMetricsRequest(name, namespace, metricName string) []byte {
	scaledRef := encodeStringField(1, name)
	if namespace != "" {
		scaledRef = append(scaledRef, encodeStringField(2, namespace)...)
	}

	var req []byte
	req = append(req, encodeMessageField(1, scaledRef)...)
	req = append(req, encodeStringField(2, metricName)...)
	return req
}

// decodeMetricValue parses a KEDA GetMetricsResponse and returns the value for
// the named metric (or the first metric when metricName is "").
//
//	message GetMetricsResponse {
//	  repeated ExternalMetric metricValues = 1;
//	}
//	message ExternalMetric {
//	  string metricName = 1;
//	  int64  metricValue = 2;
//	}
func decodeMetricValue(data []byte, metricName string) (float64, error) {
	if len(data) == 0 {
		return 0, fmt.Errorf("empty response from KEDA scaler")
	}

	i := 0
	for i < len(data) {
		fieldNum, wireType, n := decodeTag(data[i:])
		if n <= 0 {
			break
		}
		i += n

		if wireType == 2 && fieldNum == 1 {
			msgLen, n2 := decodeVarintValue(data[i:])
			if n2 <= 0 {
				break
			}
			i += n2
			if int(msgLen) > len(data[i:]) {
				break
			}
			name, value, ok := decodeExternalMetric(data[i : i+int(msgLen)])
			i += int(msgLen)
			if ok && (metricName == "" || name == metricName) {
				return float64(value), nil
			}
		} else {
			skip := skipField(data[i:], wireType)
			if skip <= 0 {
				break
			}
			i += skip
		}
	}
	return 0, fmt.Errorf("metric %q not found in KEDA response", metricName)
}

func decodeExternalMetric(data []byte) (name string, value int64, ok bool) {
	i := 0
	for i < len(data) {
		fieldNum, wireType, n := decodeTag(data[i:])
		if n <= 0 {
			break
		}
		i += n
		switch {
		case fieldNum == 1 && wireType == 2:
			strLen, n2 := decodeVarintValue(data[i:])
			if n2 <= 0 {
				return
			}
			i += n2
			if int(strLen) > len(data[i:]) {
				return
			}
			name = string(data[i : i+int(strLen)])
			i += int(strLen)
		case fieldNum == 2 && wireType == 0:
			v, n2 := decodeVarintValue(data[i:])
			if n2 <= 0 {
				return
			}
			i += n2
			value = int64(v)
		default:
			skip := skipField(data[i:], wireType)
			if skip <= 0 {
				return
			}
			i += skip
		}
	}
	ok = true
	return
}

// ── protobuf wire-format helpers ──────────────────────────────────────────────

func encodeVarint(v uint64) []byte {
	var buf [10]byte
	n := 0
	for v >= 0x80 {
		buf[n] = byte(v&0x7f) | 0x80
		v >>= 7
		n++
	}
	buf[n] = byte(v)
	return buf[:n+1]
}

func encodeStringField(fieldNum int, s string) []byte {
	tag := encodeVarint(uint64(fieldNum<<3 | 2))
	length := encodeVarint(uint64(len(s)))
	out := make([]byte, 0, len(tag)+len(length)+len(s))
	out = append(out, tag...)
	out = append(out, length...)
	out = append(out, s...)
	return out
}

func encodeMessageField(fieldNum int, msg []byte) []byte {
	tag := encodeVarint(uint64(fieldNum<<3 | 2))
	length := encodeVarint(uint64(len(msg)))
	out := make([]byte, 0, len(tag)+len(length)+len(msg))
	out = append(out, tag...)
	out = append(out, length...)
	out = append(out, msg...)
	return out
}

// decodeTag reads the field-number + wire-type varint tag. Returns (fieldNum, wireType, bytesConsumed).
func decodeTag(data []byte) (fieldNum int, wireType int, n int) {
	v, n := decodeVarintValue(data)
	if n <= 0 {
		return 0, 0, -1
	}
	return int(v >> 3), int(v & 0x7), n
}

func decodeVarintValue(data []byte) (uint64, int) {
	var x uint64
	for i, b := range data {
		if i >= 10 {
			return 0, -1
		}
		x |= uint64(b&0x7f) << (7 * uint(i))
		if b < 0x80 {
			return x, i + 1
		}
	}
	return 0, -1
}

func skipField(data []byte, wireType int) int {
	switch wireType {
	case 0: // varint
		_, n := decodeVarintValue(data)
		return n
	case 1: // 64-bit fixed
		return 8
	case 2: // length-delimited
		l, n := decodeVarintValue(data)
		if n <= 0 {
			return -1
		}
		return n + int(l)
	case 5: // 32-bit fixed
		return 4
	default:
		return -1
	}
}

// ── comparison helpers ────────────────────────────────────────────────────────

func compare(value, threshold float64, operator string) bool {
	switch operator {
	case "lte":
		return value <= threshold
	case "gte":
		return value >= threshold
	case "eq":
		return value == threshold
	default:
		return value <= threshold
	}
}

func passStr(passed bool) string {
	if passed {
		return "PASS"
	}
	return "FAIL"
}

// stripScheme removes a grpc:// or grpcs:// prefix.
func stripScheme(endpoint string) string {
	endpoint = strings.TrimPrefix(endpoint, "grpc://")
	endpoint = strings.TrimPrefix(endpoint, "grpcs://")
	return endpoint
}

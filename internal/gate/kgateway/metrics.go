package kgateway

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// GatewayMetrics holds parsed kgateway Prometheus metrics.
//
// We parse only the specific metric families we care about, avoiding the
// overhead of a full Prometheus client library.
type GatewayMetrics struct {
	// GatewayHealthy is true unless a gateway_ready=0 or similar fault is seen.
	GatewayHealthy bool

	// HTTP backend metrics: backend → count.
	backendErrors map[string]float64 // 5xx responses
	backendTotal  map[string]float64 // total requests

	// Route weight table: backend → weight (0–100).
	backendWeights map[string]int

	// AI backend metrics.
	aiP99Ms  map[string]float64 // p99 latency in milliseconds
	aiErrors map[string]float64 // error request count
	aiTotal  map[string]float64 // total request count
}

func newGatewayMetrics() *GatewayMetrics {
	return &GatewayMetrics{
		GatewayHealthy: true,
		backendErrors:  make(map[string]float64),
		backendTotal:   make(map[string]float64),
		backendWeights: make(map[string]int),
		aiP99Ms:        make(map[string]float64),
		aiErrors:       make(map[string]float64),
		aiTotal:        make(map[string]float64),
	}
}

// BackendErrorRate returns the 5xx rate for the given Envoy cluster (0.0–1.0).
func (m *GatewayMetrics) BackendErrorRate(backend string) float64 {
	total := m.backendTotal[backend]
	if total == 0 {
		return 0
	}
	return m.backendErrors[backend] / total
}

// BackendWeight returns (weight, true) when weight data exists for the backend.
func (m *GatewayMetrics) BackendWeight(backend string) (int, bool) {
	w, ok := m.backendWeights[backend]
	return w, ok
}

// AIBackendP99LatencyMs returns the p99 latency in ms for an AI backend.
func (m *GatewayMetrics) AIBackendP99LatencyMs(backend string) float64 {
	return m.aiP99Ms[backend]
}

// AIBackendErrorRate returns the error rate for an AI backend (0.0–1.0).
func (m *GatewayMetrics) AIBackendErrorRate(backend string) float64 {
	total := m.aiTotal[backend]
	if total == 0 {
		return 0
	}
	return m.aiErrors[backend] / total
}

// parsePrometheusMetrics parses the Prometheus text exposition format and
// extracts kgateway-specific metrics.
//
// Supported metric families:
//
//   - envoy_cluster_upstream_rq_xx{envoy_response_code_class="5",envoy_cluster_name="<b>"}
//     → backendErrors[b]
//
//   - envoy_cluster_upstream_rq_total{envoy_cluster_name="<b>"}
//     → backendTotal[b]
//
//   - kgateway_route_backend_weight{backend="<b>"}
//     → backendWeights[b]
//
//   - kgateway_ai_request_duration_seconds{quantile="0.99",backend="<b>"}
//     → aiP99Ms[b] (value * 1000)
//
//   - kgateway_ai_request_errors_total{backend="<b>"}
//     → aiErrors[b]
//
//   - kgateway_ai_requests_total{backend="<b>"}
//     → aiTotal[b]
//
//   - kgateway_gateway_ready{} == 0
//     → GatewayHealthy = false
func parsePrometheusMetrics(data []byte) *GatewayMetrics {
	m := newGatewayMetrics()
	scanner := bufio.NewScanner(bytes.NewReader(data))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		name, labels, value, ok := parseLine(line)
		if !ok {
			continue
		}

		switch {
		case name == "envoy_cluster_upstream_rq_xx" && labels["envoy_response_code_class"] == "5":
			if b := labels["envoy_cluster_name"]; b != "" {
				m.backendErrors[b] += value
			}

		case name == "envoy_cluster_upstream_rq_total":
			if b := labels["envoy_cluster_name"]; b != "" {
				m.backendTotal[b] += value
			}

		case name == "kgateway_route_backend_weight":
			if b := labels["backend"]; b != "" {
				m.backendWeights[b] = int(value)
			}

		case name == "kgateway_ai_request_duration_seconds" && labels["quantile"] == "0.99":
			if b := labels["backend"]; b != "" {
				m.aiP99Ms[b] = value * 1000 // seconds → milliseconds
			}

		case name == "kgateway_ai_request_errors_total":
			if b := labels["backend"]; b != "" {
				m.aiErrors[b] += value
			}

		case name == "kgateway_ai_requests_total":
			if b := labels["backend"]; b != "" {
				m.aiTotal[b] += value
			}

		case name == "kgateway_gateway_ready":
			if value == 0 {
				m.GatewayHealthy = false
			}
		}
	}

	return m
}

// parseLine parses a single Prometheus text format sample line.
// Returns (metricName, labelMap, floatValue, ok).
//
// Handles both labelled and unlabelled metrics:
//
//	some_metric{label="value"} 1.23
//	some_metric 1.23
func parseLine(line string) (name string, labels map[string]string, value float64, ok bool) {
	// Split off the value at the last space (ignoring optional timestamp).
	lastSpace := strings.LastIndex(line, " ")
	if lastSpace < 0 {
		return
	}
	valueStr := line[lastSpace+1:]
	// Drop optional timestamp field if present (second trailing token).
	if sp := strings.Index(valueStr, " "); sp >= 0 {
		valueStr = valueStr[:sp]
	}

	var err error
	value, err = strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return
	}

	nameAndLabels := strings.TrimSpace(line[:lastSpace])

	// Extract label set if present.
	lbrace := strings.Index(nameAndLabels, "{")
	if lbrace < 0 {
		name = nameAndLabels
		labels = map[string]string{}
		ok = true
		return
	}

	name = nameAndLabels[:lbrace]
	rbrace := strings.LastIndex(nameAndLabels, "}")
	if rbrace < 0 {
		return
	}

	labels = parseLabels(nameAndLabels[lbrace+1 : rbrace])
	ok = true
	return
}

// parseLabels parses the label set inside braces, e.g.:
// `key="value",key2="value2"` → map{"key":"value","key2":"value2"}
func parseLabels(s string) map[string]string {
	m := make(map[string]string)
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		eq := strings.Index(pair, "=")
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(pair[:eq])
		v := strings.Trim(strings.TrimSpace(pair[eq+1:]), `"`)
		if k != "" {
			m[k] = v
		}
	}
	return m
}

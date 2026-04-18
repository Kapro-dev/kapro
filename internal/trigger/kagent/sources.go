// Source detectors for KAgent — MLflow, OCI, Prometheus.
package kagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ── MLflow ───────────────────────────────────────────────────────────────────

// mlflowVersionsResponse is a partial unmarshal of the MLflow REST API response
// for GET /api/2.0/mlflow/model-versions/search.
type mlflowVersionsResponse struct {
	ModelVersions []struct {
		Version string `json:"version"`
		Stage   string `json:"current_stage"`
	} `json:"model_versions"`
}

// detectMLflow queries MLflow Model Registry and returns the latest version
// in the configured stage (default: Production).
func detectMLflow(ctx context.Context, c client.Client, agent *kaprov1alpha1.KAgent) (string, error) {
	src := agent.Spec.Source.MLflow
	if src == nil {
		return "", fmt.Errorf("mlflow source config is nil")
	}

	stage := src.Stage
	if stage == "" {
		stage = "Production"
	}

	token, err := readSecretToken(ctx, c, agent.Namespace, src.TokenSecretRef)
	if err != nil {
		return "", fmt.Errorf("mlflow: read token: %w", err)
	}

	url := fmt.Sprintf("%s/api/2.0/mlflow/model-versions/search?filter=name='%s'&max_results=10",
		strings.TrimRight(src.TrackingServerURL, "/"), src.ModelName)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("mlflow: build request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	hc := &http.Client{Timeout: 15 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("mlflow: GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mlflow: unexpected status %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	var result mlflowVersionsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("mlflow: unmarshal response: %w", err)
	}

	// Find the highest version number in the target stage.
	best := -1
	for _, v := range result.ModelVersions {
		if v.Stage != stage {
			continue
		}
		n, err := strconv.Atoi(v.Version)
		if err != nil {
			continue
		}
		if n > best {
			best = n
		}
	}
	if best < 0 {
		return "", nil // no version in target stage yet
	}
	return strconv.Itoa(best), nil
}

// ── OCI ──────────────────────────────────────────────────────────────────────

// detectOCI queries an OCI registry for the latest tag matching the configured pattern.
// Uses the OCI Distribution Spec v2 tags listing API.
func detectOCI(ctx context.Context, c client.Client, agent *kaprov1alpha1.KAgent) (string, error) {
	src := agent.Spec.Source.OCI
	if src == nil {
		return "", fmt.Errorf("oci source config is nil")
	}

	pattern := src.TagPattern
	if pattern == "" {
		pattern = `^v[0-9]+\.[0-9]+\.[0-9]+$`
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("oci: invalid tagPattern %q: %w", pattern, err)
	}

	// Parse repository into registry + image path.
	registry, imagePath := splitRepository(src.Repository)
	url := fmt.Sprintf("https://%s/v2/%s/tags/list", registry, imagePath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("oci: build request: %w", err)
	}

	// Auth: read dockerconfigjson secret if provided.
	if src.SecretRef != "" {
		creds, err := readDockerCreds(ctx, c, agent.Namespace, src.SecretRef, registry)
		if err == nil && creds != "" {
			req.Header.Set("Authorization", "Basic "+creds)
		}
	}

	hc := &http.Client{Timeout: 15 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("oci: GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oci: unexpected status %d from %s", resp.StatusCode, url)
	}

	var result struct {
		Tags []string `json:"tags"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("oci: unmarshal tags list: %w", err)
	}

	// Return the lexicographically greatest matching tag (works for semver v1.2.3 prefixes).
	best := ""
	for _, tag := range result.Tags {
		if re.MatchString(tag) && tag > best {
			best = tag
		}
	}
	return best, nil
}

// ── Prometheus ────────────────────────────────────────────────────────────────

// prometheusQueryResponse is a partial unmarshal of the Prometheus HTTP API response.
type prometheusQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Value []interface{} `json:"value"` // [timestamp, "value"]
		} `json:"result"`
	} `json:"data"`
}

// detectPrometheus queries Prometheus and returns the version string "triggered"
// when the metric crosses the configured threshold, or "" otherwise.
func detectPrometheus(_ context.Context, agent *kaprov1alpha1.KAgent) (string, error) {
	src := agent.Spec.Source.Prometheus
	if src == nil {
		return "", fmt.Errorf("prometheus source config is nil")
	}

	url := fmt.Sprintf("%s/api/v1/query?query=%s",
		strings.TrimRight(src.Address, "/"), src.Query)

	ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx2, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("prometheus: build request: %w", err)
	}

	hc := &http.Client{Timeout: 10 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("prometheus: GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var result prometheusQueryResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("prometheus: unmarshal: %w", err)
	}
	if result.Status != "success" {
		return "", fmt.Errorf("prometheus: query status %q", result.Status)
	}
	if len(result.Data.Result) == 0 {
		return "", nil
	}

	// Extract the numeric value from the first result.
	valueStr, ok := result.Data.Result[0].Value[1].(string)
	if !ok {
		return "", fmt.Errorf("prometheus: unexpected value type")
	}
	val, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return "", fmt.Errorf("prometheus: parse value %q: %w", valueStr, err)
	}

	triggered := evalThreshold(val, src.Operator, src.Threshold)
	if !triggered {
		return "", nil
	}
	// Version string for Prometheus trigger: "triggered-{timestamp}".
	// The KAgent deduplicates on status.lastVersion — so this fires once per threshold crossing.
	return fmt.Sprintf("triggered-%d", time.Now().Unix()), nil
}

// evalThreshold compares val op threshold.
func evalThreshold(val float64, op string, threshold float64) bool {
	switch op {
	case ">":
		return val > threshold
	case ">=":
		return val >= threshold
	case "<":
		return val < threshold
	case "<=":
		return val <= threshold
	case "==":
		return val == threshold
	}
	return false
}

// ── helpers ───────────────────────────────────────────────────────────────────

func readSecretToken(ctx context.Context, c client.Client, ns, secretRef string) (string, error) {
	if secretRef == "" {
		return "", nil
	}
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: secretRef}, &secret); err != nil {
		return "", err
	}
	return string(secret.Data["token"]), nil
}

func readDockerCreds(ctx context.Context, c client.Client, ns, secretRef, registry string) (string, error) {
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: secretRef}, &secret); err != nil {
		return "", err
	}
	raw, ok := secret.Data[".dockerconfigjson"]
	if !ok {
		return "", fmt.Errorf("secret %s missing .dockerconfigjson", secretRef)
	}
	var cfg struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", err
	}
	for host, entry := range cfg.Auths {
		if strings.Contains(host, registry) || strings.Contains(registry, host) {
			return entry.Auth, nil
		}
	}
	return "", nil
}

// splitRepository splits "registry.example.com/org/image" into ("registry.example.com", "org/image").
func splitRepository(repo string) (registry, imagePath string) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "index.docker.io", repo
}

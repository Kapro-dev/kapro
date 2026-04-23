// Package webhook implements the HTTP webhook gate.
//
// Kapro calls an external HTTP endpoint and interprets the JSON response
// to determine whether the gate passed, failed, or is still running.
// The webhook gate is the escape hatch for teams with bespoke gate logic
// that does not fit CEL, Argo Analysis, or Job-based evaluation.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	pkggate "kapro.io/kapro/pkg/gate"
)

// Gate implements the webhook gate type.
// It sends a POST request to the configured URL with the gate request payload
// and reads back a GateResult JSON response.
type Gate struct {
	// HTTPClient is the HTTP client to use for requests.
	// When nil, http.DefaultClient is used with a 30s timeout.
	HTTPClient *http.Client
}

// webhookPayload is the JSON body sent to the webhook endpoint.
type webhookPayload struct {
	Promotion string            `json:"promotion"`
	Target    string            `json:"target"`
	Version   string            `json:"version"`
	Release   string            `json:"release"`
	Args      map[string]string `json:"args"`
}

// webhookResponse is the expected JSON response from the webhook endpoint.
type webhookResponse struct {
	// Phase is one of: Passed, Failed, Running, Inconclusive.
	Phase   string `json:"phase"`
	Message string `json:"message"`
	// RetryAfter is an optional hint for when to retry (e.g. "30s").
	RetryAfter string `json:"retryAfter,omitempty"`
}

// Evaluate sends the gate request to the configured webhook URL and returns a GateResult.
func (g *Gate) Evaluate(ctx context.Context, req pkggate.Request) (pkggate.Result, error) {
	if req.Template == nil || req.Template.Webhook == nil {
		return pkggate.Result{}, fmt.Errorf("webhook gate: template Webhook spec is nil")
	}
	url := req.Template.Webhook.URL
	if url == "" {
		return pkggate.Result{}, fmt.Errorf("webhook gate: template Webhook URL is empty")
	}

	var promotion, target, version, release string
	if req.Context != nil {
		promotion = req.Context.Name
		target = req.Context.Target
		version = req.Context.Version
		release = req.Context.ReleaseRef
	}

	payload := webhookPayload{
		Promotion: promotion,
		Target:    target,
		Version:   version,
		Release:   release,
		Args:      req.Args,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return pkggate.Result{}, fmt.Errorf("webhook gate: marshal payload: %w", err)
	}

	hc := g.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return pkggate.Result{}, fmt.Errorf("webhook gate: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(httpReq)
	if err != nil {
		return pkggate.Result{}, fmt.Errorf("webhook gate: call %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return pkggate.Result{}, fmt.Errorf("webhook gate: server error %d from %s", resp.StatusCode, url)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return pkggate.Result{}, fmt.Errorf("webhook gate: read response: %w", err)
	}

	var wr webhookResponse
	if err := json.Unmarshal(respBody, &wr); err != nil {
		return pkggate.Result{}, fmt.Errorf("webhook gate: unmarshal response: %w", err)
	}

	return pkggate.Result{
		Phase:      kaprov1alpha1.GatePhase(wr.Phase),
		Message:    wr.Message,
		RetryAfter: wr.RetryAfter,
	}, nil
}

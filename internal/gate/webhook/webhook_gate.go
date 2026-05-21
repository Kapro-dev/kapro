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
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	pkggate "kapro.io/kapro/pkg/gate"
)

// isForbiddenIP returns true for addresses that must not be reached via the webhook gate:
// loopback, private, link-local, unspecified (0.0.0.0 / ::), and multicast.
// Addresses are first unmapped (IPv4-in-IPv6 → IPv4) so every check is canonical.
func isForbiddenIP(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsUnspecified() || addr.IsMulticast() || addr.IsLinkLocalMulticast()
}

// safeDial resolves host and rejects connections to any private / forbidden address.
// It tries all valid resolved IPs in order so that public multi-homed endpoints still work.
func safeDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("ssrf guard: parse addr %q: %w", addr, err)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("ssrf guard: resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("ssrf guard: no addresses for %q", host)
	}
	for _, ip := range ips {
		a, ok := netip.AddrFromSlice(ip.IP)
		if !ok {
			return nil, fmt.Errorf("ssrf guard: invalid IP %v", ip.IP)
		}
		if isForbiddenIP(a) {
			return nil, fmt.Errorf("ssrf guard: %q resolves to forbidden address %s", host, ip.IP)
		}
	}
	// All IPs cleared; try them in order.
	d := &net.Dialer{}
	var lastErr error
	for _, ip := range ips {
		conn, dialErr := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

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
	Promotion    string            `json:"promotion"`
	Target       string            `json:"target"`
	Version      string            `json:"version"`
	PromotionRun string            `json:"promotionrun"`
	Args         map[string]string `json:"args"`
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
	rawURL := req.Template.Webhook.URL
	if rawURL == "" {
		return pkggate.Result{}, fmt.Errorf("webhook gate: template Webhook URL is empty")
	}
	// Validate scheme and host before dialing.
	parsedURL, err := url.Parse(rawURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return pkggate.Result{}, fmt.Errorf("webhook gate: URL must be http or https with a non-empty host: %q", rawURL)
	}

	var promotion, target, version, promotionrun string
	if req.Context != nil {
		promotion = req.Context.Name
		target = req.Context.Target
		version = req.Context.Version
		promotionrun = req.Context.PromotionRunRef
	}

	payload := webhookPayload{
		Promotion:    promotion,
		Target:       target,
		Version:      version,
		PromotionRun: promotionrun,
		Args:         req.Args,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return pkggate.Result{}, fmt.Errorf("webhook gate: marshal payload: %w", err)
	}

	hc := g.HTTPClient
	if hc == nil {
		// Clone the default transport (preserves HTTP/2, keep-alive, etc.) but
		// override DialContext to block SSRF targets and clear the proxy so private
		// addresses cannot be reached via environment proxy variables.
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.DialContext = safeDial
		t.Proxy = nil
		hc = &http.Client{Timeout: 30 * time.Second, Transport: t}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return pkggate.Result{}, fmt.Errorf("webhook gate: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(httpReq)
	if err != nil {
		return pkggate.Result{}, fmt.Errorf("webhook gate: call %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 500 {
		return pkggate.Result{}, fmt.Errorf("webhook gate: server error %d from %s", resp.StatusCode, rawURL)
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
		Phase:      kaprov1alpha2.GatePhase(wr.Phase),
		Message:    wr.Message,
		RetryAfter: wr.RetryAfter,
		Evidence: []pkggate.Evidence{{
			Type:   "webhook",
			Reason: wr.Message,
		}},
	}, nil
}

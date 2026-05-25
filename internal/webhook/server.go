// Package webhook provides an HTTP server for human approval of Kapro promotionruns.
//
// The server exposes three endpoints:
//
//	GET  /approve/{targetKey}#token=<t>   — renders a confirmation page; the fragment is browser-only.
//	POST /approve/{targetKey}             — creates an Approval CR using Authorization: Bearer <t>.
//	GET  /reject/{targetKey}#token=<t>    — renders a confirmation page; the fragment is browser-only.
//	POST /reject/{targetKey}              — rejects the target using Authorization: Bearer <t>.
//	GET  /status/{targetKey}?ns=<ns>      — returns public target phase/version (no auth required)
//
// Token format is defined in internal/webhook/token. Tokens are HMAC-SHA256 signed,
// scoped to a single PromotionRun UID + target key, and expire after 48 hours by default.
//
// The server creates Approval objects directly — no gRPC or extra dependencies.
// Any notification channel (email, Teams, webhook, etc.) delivers the approve/reject
// URLs; the channel is irrelevant to this server.
package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/internal/webhook/token"
)

// Server handles approve/reject/status HTTP requests for Kapro promotionrun approvals.
type Server struct {
	// Client is used to look up PromotionRuns and create Approval CRs.
	Client client.Client
	// DecisionReader is used for Decision API read paths. In production this is
	// the manager APIReader so bounded list calls use Kubernetes API pagination
	// instead of walking the controller-runtime cache.
	DecisionReader client.Reader
	// TokenSecret is the HMAC key used to verify approval tokens.
	// Must match the secret used by PromotionRunReconciler to sign tokens.
	TokenSecret []byte
	// OperatorNamespace is the namespace in which PromotionRuns are managed.
	// Defaults to "kapro-system" if empty.
	OperatorNamespace string
	// DecisionAPIEnabled controls whether /api/v1 Decision API routes are mounted.
	DecisionAPIEnabled bool
	// DecisionAuthenticator validates bearer tokens for Decision API routes.
	DecisionAuthenticator DecisionAuthenticator
	// DecisionAuthorizer checks Kubernetes RBAC for Decision API actions.
	DecisionAuthorizer DecisionAuthorizer
}

// Handler returns the HTTP mux for all approval and Decision API endpoints.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/approve/", s.handleApprove)
	mux.HandleFunc("/reject/", s.handleReject)
	mux.HandleFunc("/status/", s.handleStatus)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if s.DecisionAPIEnabled {
		s.RegisterDecisionAPI(mux)
	}
	return mux
}

// handleApprove verifies the bearer token and creates an Approval CR.
// POST /approve/{targetKey}
func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.renderDecisionPage(w, "approve", trimPrefix(r.URL.Path, "/approve/"))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Bound all Kubernetes API calls so a slow hub never hangs the goroutine.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	targetKey := r.PathValue("name")
	if targetKey == "" {
		targetKey = trimPrefix(r.URL.Path, "/approve/")
	}

	claims, err := s.verifyToken(r, "approve")
	if err != nil {
		log.FromContext(ctx).Info("approve: invalid token", "error", err)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	// Look up the PromotionRun and validate UID.
	promotionrun, err := s.getPromotionRun(ctx, claims.PromotionRun, claims.Namespace)
	if err != nil {
		http.Error(w, "promotionrun not found", http.StatusNotFound)
		return
	}
	expectedUID := string(promotionrun.UID) + "/" + targetKey
	if expectedUID != claims.UID {
		http.Error(w, "token bound to different promotionrun instance", http.StatusConflict)
		return
	}
	var target kaproruntimev1alpha1.Target
	if err := s.Client.Get(ctx, client.ObjectKey{Name: targetKey}, &target); err != nil {
		http.Error(w, "target entry not found", http.StatusNotFound)
		return
	}
	if target.Spec.PromotionRunRef != claims.PromotionRun {
		http.Error(w, "target/promotionrun mismatch", http.StatusConflict)
		return
	}

	approval := s.buildApproval(claims)
	if err := s.Client.Create(ctx, approval); err != nil {
		if isAlreadyExists(err) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"status": "already_approved",
			})
			return
		}
		log.FromContext(ctx).Error(err, "create Approval CR failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	log.FromContext(ctx).Info("Approval CR created",
		"targetKey", targetKey,
		"promotionrun", claims.PromotionRun,
		"target", claims.Target,
	)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "approved",
		"targetKey": targetKey,
		"target":    claims.Target,
		"version":   claims.Version,
	})
}

// handleReject sets rejected=true on the inline target entry so PromotionRunReconciler
// fails it on the next reconcile.
// POST /reject/{targetKey}
func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.renderDecisionPage(w, "reject", trimPrefix(r.URL.Path, "/reject/"))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Bound all Kubernetes API calls so a slow hub never hangs the goroutine.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	targetKey := r.PathValue("name")
	if targetKey == "" {
		targetKey = trimPrefix(r.URL.Path, "/reject/")
	}

	claims, err := s.verifyToken(r, "reject")
	if err != nil {
		log.FromContext(ctx).Info("reject: invalid token", "error", err)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	promotionrun, err := s.getPromotionRun(ctx, claims.PromotionRun, claims.Namespace)
	if err != nil {
		http.Error(w, "promotionrun not found", http.StatusNotFound)
		return
	}
	expectedUID := string(promotionrun.UID) + "/" + targetKey
	if expectedUID != claims.UID {
		http.Error(w, "token bound to different promotionrun instance", http.StatusConflict)
		return
	}
	var target kaproruntimev1alpha1.Target
	if err := s.Client.Get(ctx, client.ObjectKey{Name: targetKey}, &target); err != nil {
		http.Error(w, "target entry not found", http.StatusNotFound)
		return
	}
	if target.Spec.PromotionRunRef != claims.PromotionRun {
		http.Error(w, "target/promotionrun mismatch", http.StatusConflict)
		return
	}

	// Use the identity embedded in the verified HMAC token — not the raw query string.
	// The query string ?by= parameter is unauthenticated and can be trivially spoofed.
	rejectedBy := claims.ApprovedBy
	if rejectedBy == "" {
		rejectedBy = "webhook"
	}

	// Idempotency: return 409 if already rejected (mirrors the approve endpoint).
	if target.Status.Rejected {
		writeJSON(w, http.StatusConflict, map[string]string{
			"status":    "already-rejected",
			"targetKey": targetKey,
			"target":    claims.Target,
		})
		return
	}

	// Patch only the target status fields.
	patch := client.MergeFrom(target.DeepCopy())
	target.Status.Rejected = true
	target.Status.RejectedBy = rejectedBy
	if err := s.Client.Status().Patch(ctx, &target, patch); err != nil {
		log.FromContext(ctx).Error(err, "patch PromotionTarget rejection failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	log.FromContext(ctx).Info("target rejection set",
		"targetKey", targetKey,
		"rejectedBy", rejectedBy,
	)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "rejected",
		"targetKey": targetKey,
		"target":    claims.Target,
	})
}

// handleStatus returns the public target phase. No authentication required — only
// phase and version are exposed (no secrets or user data).
// GET /status/{targetKey}
//
// The operator namespace is fixed at startup. The endpoint verifies that the
// target belongs to a PromotionRun in that namespace before returning public status.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	targetKey := r.PathValue("name")
	if targetKey == "" {
		targetKey = trimPrefix(r.URL.Path, "/status/")
	}

	ns := s.OperatorNamespace
	if ns == "" {
		ns = "kapro-system"
	}

	var target kaproruntimev1alpha1.Target
	if err := s.Client.Get(r.Context(), client.ObjectKey{Name: targetKey}, &target); err != nil {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	if target.Spec.PromotionRunRef == "" {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	if _, err := s.getPromotionRun(r.Context(), target.Spec.PromotionRunRef, ns); err != nil {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"phase":        string(target.Status.Phase),
		"version":      target.Spec.Version,
		"target":       target.Spec.Target,
		"promotionrun": target.Spec.PromotionRunRef,
	})
}

// maxApprovalTokenLen bounds the Authorization bearer token so an attacker
// cannot force verification work on arbitrary-size inputs. Real Kapro
// approval tokens are HMAC-signed claims that decode to <1 KiB; 4 KiB leaves
// headroom for future fields without crossing into pathological territory.
const maxApprovalTokenLen = 4 * 1024

func (s *Server) verifyToken(r *http.Request, expectedAction string) (*token.Claims, error) {
	if r.URL.RawQuery != "" {
		return nil, fmt.Errorf("approval tokens must be sent in Authorization bearer headers, not query strings")
	}
	auth := r.Header.Get("Authorization")
	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(auth, bearerPrefix) {
		return nil, fmt.Errorf("missing bearer token")
	}
	t := strings.TrimSpace(strings.TrimPrefix(auth, bearerPrefix))
	if len(t) > maxApprovalTokenLen {
		return nil, fmt.Errorf("token too large")
	}
	claims, err := token.Verify(t, s.TokenSecret)
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}
	if claims.Action != expectedAction {
		return nil, fmt.Errorf("token action %q cannot be used for %s", claims.Action, expectedAction)
	}
	return claims, nil
}

var decisionPageTemplate = template.Must(template.New("approval-decision").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Kapro {{.TitleAction}}</title>
</head>
<body style="font-family:system-ui,sans-serif;max-width:520px;margin:48px auto;padding:0 20px;line-height:1.45">
  <h1>Kapro {{.TitleAction}}</h1>
  <p>Target: <code>{{.TargetKey}}</code></p>
  <p id="state">Review this action before continuing.</p>
  <button id="submit" type="button">{{.TitleAction}}</button>
  <pre id="result" style="white-space:pre-wrap"></pre>
  <script>
  const token = new URLSearchParams(window.location.hash.slice(1)).get("token");
  const state = document.getElementById("state");
  const submit = document.getElementById("submit");
  const result = document.getElementById("result");
  if (!token) {
    state.textContent = "This approval link is missing its browser-only token fragment.";
    submit.disabled = true;
  }
  submit.addEventListener("click", async () => {
    submit.disabled = true;
    state.textContent = "Submitting decision...";
    const response = await fetch(window.location.pathname, {
      method: "POST",
      headers: { "Authorization": "Bearer " + token }
    });
    const text = await response.text();
    state.textContent = response.ok ? "Decision recorded." : "Decision failed.";
    result.textContent = text;
  });
  </script>
</body>
</html>`))

func (s *Server) renderDecisionPage(w http.ResponseWriter, action, targetKey string) {
	titleAction := "Approve"
	if action == "reject" {
		titleAction = "Reject"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := decisionPageTemplate.Execute(w, map[string]string{
		"TargetKey":   targetKey,
		"TitleAction": titleAction,
	}); err != nil {
		http.Error(w, "render decision page", http.StatusInternalServerError)
	}
}

func (s *Server) getPromotionRun(ctx context.Context, name, namespace string) (*kaproruntimev1alpha1.PromotionRun, error) {
	var promotionrun kaproruntimev1alpha1.PromotionRun
	if err := s.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &promotionrun); err != nil {
		return nil, err
	}
	return &promotionrun, nil
}

func (s *Server) buildApproval(claims *token.Claims) *kaprov1alpha1.Approval {
	approvedBy := claims.ApprovedBy
	if approvedBy == "" {
		approvedBy = "webhook"
	}
	return &kaprov1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{
			// Name is deterministic: one cluster-scoped Approval per
			// (promotionrun, ref) pair, where ref is the rollout entry sync name.
			Name: fmt.Sprintf("%s-%s", claims.PromotionRun, claims.SyncName),
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			PromotionRun: claims.PromotionRun,
			Target:       claims.Target,
			Ref:          claims.SyncName,
			ApprovedBy:   approvedBy,
			Comment:      fmt.Sprintf("approved via webhook for version %s", claims.Version),
		},
	}
}

func isAlreadyExists(err error) bool {
	return err != nil && apierrors.IsAlreadyExists(err)
}

func trimPrefix(s, prefix string) string {
	if len(s) > len(prefix) {
		return s[len(prefix):]
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

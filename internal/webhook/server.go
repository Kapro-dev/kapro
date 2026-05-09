// Package webhook provides an HTTP server for human approval of Kapro releases.
//
// The server exposes three endpoints:
//
//	POST /approve/{targetKey}?token=<t>   — creates an Approval CR to unblock the target
//	POST /reject/{targetKey}?token=<t>    — sets rejected=true on the inline target status entry;
//	                                        ReleaseReconciler will fail the target on next reconcile.
//	GET  /status/{targetKey}?ns=<ns>      — returns public target phase/version (no auth required)
//
// Token format is defined in internal/webhook/token. Tokens are HMAC-SHA256 signed,
// scoped to a single Release UID + target key, and expire after 48 hours by default.
//
// The server creates Approval objects directly — no gRPC or extra dependencies.
// Any notification channel (email, Teams, webhook, etc.) delivers the approve/reject
// URLs; the channel is irrelevant to this server.
package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/webhook/token"
)

// Server handles approve/reject/status HTTP requests for Kapro release approvals.
type Server struct {
	// Client is used to look up Releases and create Approval CRs.
	Client client.Client
	// TokenSecret is the HMAC key used to verify approval tokens.
	// Must match the secret used by ReleaseReconciler to sign tokens.
	TokenSecret []byte
	// OperatorNamespace is the namespace in which Releases are managed.
	// Defaults to "kapro-system" if empty.
	OperatorNamespace string
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
	s.RegisterDecisionAPI(mux)
	return mux
}

// handleApprove verifies the token and creates an Approval CR.
// POST /approve/{targetKey}?token=<t>
func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
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

	// Look up the Release and validate UID.
	release, err := s.getRelease(ctx, claims.Release, claims.Namespace)
	if err != nil {
		http.Error(w, "release not found", http.StatusNotFound)
		return
	}
	expectedUID := string(release.UID) + "/" + targetKey
	if expectedUID != claims.UID {
		http.Error(w, "token bound to different release instance", http.StatusConflict)
		return
	}
	var target kaprov1alpha1.ReleaseTarget
	if err := s.Client.Get(ctx, client.ObjectKey{Name: targetKey}, &target); err != nil {
		http.Error(w, "target entry not found", http.StatusNotFound)
		return
	}
	if target.Spec.ReleaseRef != claims.Release {
		http.Error(w, "target/release mismatch", http.StatusConflict)
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
		"release", claims.Release,
		"target", claims.Target,
	)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "approved",
		"targetKey": targetKey,
		"target":    claims.Target,
		"version":   claims.Version,
	})
}

// handleReject sets rejected=true on the inline env entry so ReleaseReconciler
// fails it on the next reconcile.
// POST /reject/{envKey}?token=<t>
func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
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

	release, err := s.getRelease(ctx, claims.Release, claims.Namespace)
	if err != nil {
		http.Error(w, "release not found", http.StatusNotFound)
		return
	}
	expectedUID := string(release.UID) + "/" + targetKey
	if expectedUID != claims.UID {
		http.Error(w, "token bound to different release instance", http.StatusConflict)
		return
	}
	var target kaprov1alpha1.ReleaseTarget
	if err := s.Client.Get(ctx, client.ObjectKey{Name: targetKey}, &target); err != nil {
		http.Error(w, "target entry not found", http.StatusNotFound)
		return
	}
	if target.Spec.ReleaseRef != claims.Release {
		http.Error(w, "target/release mismatch", http.StatusConflict)
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
		log.FromContext(ctx).Error(err, "patch ReleaseTarget rejection failed")
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
// target belongs to a Release in that namespace before returning public status.
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

	var target kaprov1alpha1.ReleaseTarget
	if err := s.Client.Get(r.Context(), client.ObjectKey{Name: targetKey}, &target); err != nil {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	if target.Spec.ReleaseRef == "" {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	if _, err := s.getRelease(r.Context(), target.Spec.ReleaseRef, ns); err != nil {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"phase":   string(target.Status.Phase),
		"version": target.Spec.Version,
		"target":  target.Spec.Target,
		"release": target.Spec.ReleaseRef,
	})
}

func (s *Server) verifyToken(r *http.Request, expectedAction string) (*token.Claims, error) {
	t := r.URL.Query().Get("token")
	if t == "" {
		return nil, fmt.Errorf("missing token")
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

func (s *Server) getRelease(ctx context.Context, name, namespace string) (*kaprov1alpha1.Release, error) {
	var release kaprov1alpha1.Release
	if err := s.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &release); err != nil {
		return nil, err
	}
	return &release, nil
}

func (s *Server) buildApproval(claims *token.Claims) *kaprov1alpha1.Approval {
	approvedBy := claims.ApprovedBy
	if approvedBy == "" {
		approvedBy = "webhook"
	}
	return &kaprov1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{
			// Name is deterministic: one cluster-scoped Approval per
			// (release, ref) pair, where ref is the rollout entry sync name.
			Name: fmt.Sprintf("%s-%s", claims.Release, claims.SyncName),
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			Release:    claims.Release,
			Target:     claims.Target,
			Ref:        claims.SyncName,
			ApprovedBy: approvedBy,
			Comment:    fmt.Sprintf("approved via webhook for version %s", claims.Version),
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

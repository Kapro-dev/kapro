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
}

// Handler returns the HTTP mux for all approval endpoints.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/approve/", s.handleApprove)
	mux.HandleFunc("/reject/", s.handleReject)
	mux.HandleFunc("/status/", s.handleStatus)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// handleApprove verifies the token and creates an Approval CR.
// POST /approve/{targetKey}?token=<t>
func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	targetKey := r.PathValue("name")
	if targetKey == "" {
		targetKey = trimPrefix(r.URL.Path, "/approve/")
	}

	claims, err := s.verifyToken(r, "approve")
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	// Look up the Release and validate UID.
	release, err := s.getRelease(r.Context(), claims.Release, claims.Namespace)
	if err != nil {
		http.Error(w, "release not found", http.StatusNotFound)
		return
	}
	expectedUID := string(release.UID) + "/" + targetKey
	if expectedUID != claims.UID {
		http.Error(w, "token bound to different release instance", http.StatusConflict)
		return
	}

	approval := s.buildApproval(claims)
	if err := s.Client.Create(r.Context(), approval); err != nil {
		if isAlreadyExists(err) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"status": "already_approved",
			})
			return
		}
		log.FromContext(r.Context()).Error(err, "create Approval CR failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	log.FromContext(r.Context()).Info("Approval CR created",
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

	targetKey := r.PathValue("name")
	if targetKey == "" {
		targetKey = trimPrefix(r.URL.Path, "/reject/")
	}

	claims, err := s.verifyToken(r, "reject")
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	release, err := s.getRelease(r.Context(), claims.Release, claims.Namespace)
	if err != nil {
		http.Error(w, "release not found", http.StatusNotFound)
		return
	}
	expectedUID := string(release.UID) + "/" + targetKey
	if expectedUID != claims.UID {
		http.Error(w, "token bound to different release instance", http.StatusConflict)
		return
	}

	// Find the target entry and mark it rejected.
	i := findTargetByKey(release, targetKey)
	if i < 0 {
		http.Error(w, "target entry not found in release status", http.StatusNotFound)
		return
	}

	rejectedBy := r.URL.Query().Get("by")
	if rejectedBy == "" {
		rejectedBy = "webhook"
	}

	// Patch only the target status fields.
	patch := client.MergeFrom(release.DeepCopy())
	release.Status.Targets[i].Rejected = true
	release.Status.Targets[i].RejectedBy = rejectedBy
	if err := s.Client.Status().Patch(r.Context(), release, patch); err != nil {
		log.FromContext(r.Context()).Error(err, "patch Release target rejection failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	log.FromContext(r.Context()).Info("target rejection set",
		"targetKey", targetKey,
		"rejectedBy", rejectedBy,
	)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "rejected",
		"targetKey": targetKey,
		"target":    claims.Target,
	})
}

// handleStatus returns the public target phase. No auth required.
// GET /status/{targetKey}?ns=<namespace>
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	targetKey := r.PathValue("name")
	if targetKey == "" {
		targetKey = trimPrefix(r.URL.Path, "/status/")
	}
	ns := r.URL.Query().Get("ns")
	if ns == "" {
		ns = "kapro-system"
	}

	// Scan all Releases in the namespace for a target entry matching targetKey.
	var releaseList kaprov1alpha1.ReleaseList
	if err := s.Client.List(r.Context(), &releaseList,
		client.InNamespace(ns), client.Limit(500),
	); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for i := range releaseList.Items {
		rel := &releaseList.Items[i]
		idx := findTargetByKey(rel, targetKey)
		if idx < 0 {
			continue
		}
		target := rel.Status.Targets[idx]
		writeJSON(w, http.StatusOK, map[string]string{
			"phase":   string(target.Phase),
			"version": target.Version,
			"target":  target.Target,
			"release": rel.Name,
		})
		return
	}

	http.Error(w, "target not found", http.StatusNotFound)
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

// findTargetByKey finds the index of the target entry in release.Status.Targets
// whose computed key matches targetKey. Returns -1 if not found.
func findTargetByKey(release *kaprov1alpha1.Release, targetKey string) int {
	for i, target := range release.Status.Targets {
		k := fmt.Sprintf("%s-%s-%s-%s", release.Name, target.PipelineRef, target.Stage, target.Target)
		if k == targetKey {
			return i
		}
	}
	return -1
}

func (s *Server) buildApproval(claims *token.Claims) *kaprov1alpha1.Approval {
	approvedBy := claims.ApprovedBy
	if approvedBy == "" {
		approvedBy = "webhook"
	}
	return &kaprov1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{
			// Name is deterministic: one approval per release+target combination.
			Name:      fmt.Sprintf("%s-%s", claims.Release, claims.Target),
			Namespace: claims.Namespace,
			Labels: map[string]string{
				"kapro.io/release": claims.Release,
				"kapro.io/target":  claims.Target,
			},
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			Kind:       kaprov1alpha1.ApprovalKindSync,
			Ref:        claims.Target,
			Release:    claims.Release,
			Target:     claims.Target,
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

//
// The server exposes three endpoints:
//
//   POST /approve/{name}?token=<t>   — creates an Approval CR to unblock a target rollout
//   POST /reject/{name}?token=<t>    — patches the owning Release with a rejection annotation;
//                                      the release controller will fail the rollout on the next
//                                      reconcile, preserving controller invariants.
//   GET  /status/{name}?ns=<ns>      — returns public rollout phase/version (no auth required)
//
// Token format is defined in internal/webhook/token. Tokens are HMAC-SHA256 signed,
// scoped to a single rollout entry UID surrogate, and expire after 48 hours by default.
//
// The server creates Approval objects directly — no gRPC or extra dependencies.
// Any notification channel (email, Teams, webhook, etc.) delivers the approve/reject
// URLs; the channel is irrelevant to this server.

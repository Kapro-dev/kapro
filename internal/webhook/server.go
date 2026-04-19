// Package webhook provides an HTTP server for human approval of Kapro Syncs.
//
// The server exposes three endpoints:
//
//   POST /approve/{name}?token=<t>   — creates an Approval CR to unblock the Sync
//   POST /reject/{name}?token=<t>    — patches the Sync with a rejection annotation;
//                                      the SyncReconciler will call failSync() on
//                                      the next reconcile, preserving all controller invariants.
//   GET  /status/{name}?ns=<ns>      — returns public Sync phase/version (no auth required)
//
// Token format is defined in internal/webhook/token. Tokens are HMAC-SHA256 signed,
// scoped to a single Sync UID, and expire after 48 hours by default.
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

// AnnotationRejected is patched on a Sync when a human POSTs to /reject.
// The SyncReconciler checks this in handleWaitingApproval and calls failSync.
const (
	AnnotationRejected   = "kapro.io/rejected"
	AnnotationRejectedBy = "kapro.io/rejected-by"
)

// Server handles approve/reject/status HTTP requests for Kapro Syncs.
type Server struct {
	// Client is used to look up Syncs and create Approval CRs.
	Client client.Client
	// TokenSecret is the HMAC key used to verify approval tokens.
	// Must match the secret used by SyncReconciler to sign tokens.
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
// POST /approve/{name}?token=<t>
func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	syncName := r.PathValue("name")
	if syncName == "" {
		syncName = trimPrefix(r.URL.Path, "/approve/")
	}

	claims, err := s.verifyToken(r, "approve")
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	// Look up the Sync to get its namespace and validate UID.
	sync, err := s.getSync(r.Context(), syncName, claims.Namespace)
	if err != nil {
		http.Error(w, "sync not found", http.StatusNotFound)
		return
	}
	if string(sync.UID) != claims.UID {
		http.Error(w, "token bound to different sync instance", http.StatusConflict)
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
		"sync", syncName,
		"approvedBy", claims.UID,
		"environment", claims.Environment,
	)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":      "approved",
		"sync":        syncName,
		"environment": claims.Environment,
		"version":     claims.Version,
	})
}

// handleReject patches the Sync with a rejection annotation.
// The SyncReconciler will call failSync() on next reconcile.
// POST /reject/{name}?token=<t>
func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	syncName := r.PathValue("name")
	if syncName == "" {
		syncName = trimPrefix(r.URL.Path, "/reject/")
	}

	claims, err := s.verifyToken(r, "reject")
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	sync, err := s.getSync(r.Context(), syncName, claims.Namespace)
	if err != nil {
		http.Error(w, "sync not found", http.StatusNotFound)
		return
	}
	if string(sync.UID) != claims.UID {
		http.Error(w, "token bound to different sync instance", http.StatusConflict)
		return
	}

	// Patch annotations — the controller owns the status transition.
	patch := client.MergeFrom(sync.DeepCopy())
	if sync.Annotations == nil {
		sync.Annotations = make(map[string]string)
	}
	sync.Annotations[AnnotationRejected] = "true"
	// Use a query param ?by=<name> for the approver identity if provided.
	rejectedBy := r.URL.Query().Get("by")
	if rejectedBy == "" {
		rejectedBy = "webhook"
	}
	sync.Annotations[AnnotationRejectedBy] = rejectedBy

	if err := s.Client.Patch(r.Context(), sync, patch); err != nil {
		log.FromContext(r.Context()).Error(err, "patch Sync rejection annotation failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	log.FromContext(r.Context()).Info("Sync rejection annotated",
		"sync", syncName,
		"rejectedBy", rejectedBy,
	)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":      "rejected",
		"sync":        syncName,
		"environment": claims.Environment,
	})
}

// handleStatus returns the public sync phase. No auth required.
// GET /status/{name}?ns=<namespace>
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	syncName := r.PathValue("name")
	if syncName == "" {
		syncName = trimPrefix(r.URL.Path, "/status/")
	}
	ns := r.URL.Query().Get("ns")
	if ns == "" {
		ns = "kapro-system"
	}

	sync, err := s.getSync(r.Context(), syncName, ns)
	if err != nil {
		http.Error(w, "sync not found", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"phase":       string(sync.Status.Phase),
		"version":     sync.Spec.Version,
		"environment": sync.Spec.EnvironmentRef,
		"release":     sync.Spec.ReleaseRef,
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

func (s *Server) getSync(ctx context.Context, name, namespace string) (*kaprov1alpha1.Sync, error) {
	var sync kaprov1alpha1.Sync
	if err := s.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &sync); err != nil {
		return nil, err
	}
	return &sync, nil
}

func (s *Server) buildApproval(claims *token.Claims) *kaprov1alpha1.Approval {
	approvedBy := claims.ApprovedBy
	if approvedBy == "" {
		approvedBy = "webhook" // fallback for tokens minted before ApprovedBy was added
	}
	return &kaprov1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{
			// Name is deterministic: one approval per release+env combination.
			Name:      fmt.Sprintf("%s-%s", claims.Release, claims.Environment),
			Namespace: claims.Namespace,
			Labels: map[string]string{
				"kapro.io/release":     claims.Release,
				"kapro.io/environment": claims.Environment,
			},
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			Kind:           kaprov1alpha1.ApprovalKindSync,
			Ref:            claims.Environment,
			Release:        claims.Release,
			EnvironmentRef: claims.Environment,
			ApprovedBy:     approvedBy,
			Comment:        fmt.Sprintf("approved via webhook for version %s", claims.Version),
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

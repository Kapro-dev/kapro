// Package registration implements the Kapro cluster self-registration HTTP server.
// Inspired by: OCM klusterlet registration controller, Kubernetes TLS bootstrap token server.
//
// Security model:
//   - Operator stores only SHA-256(token) in BootstrapToken CR — plaintext never persisted
//   - cluster-controller POSTs raw token + clusterName to /register
//   - Server hashes the raw token, finds matching BootstrapToken by hash
//   - Only then issues a scoped ServiceAccount token (1h, auto-renewing)
//   - Bootstrap token is marked used (one-time) before responding
package registration

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const (
	// KaproSystemNamespace is where operator-managed ServiceAccounts live.
	KaproSystemNamespace = "kapro-system"
	// TokenExpirySeconds — issued SA tokens are valid for 1 hour; cluster-controller renews before expiry.
	TokenExpirySeconds = int64(3600)
)

// RegisterRequest is the JSON body POSTed by cluster-controller to /register.
type RegisterRequest struct {
	ClusterName string `json:"clusterName"`
	Token       string `json:"token"` // raw plaintext token — hashed by server, never logged or stored
}

// RegisterResponse is returned on successful registration.
type RegisterResponse struct {
	// SAToken is the short-lived ServiceAccount token for management cluster auth.
	SAToken string `json:"saToken"`
	// ExpiresAt is when the SAToken expires (RFC3339). cluster-controller must renew before this.
	ExpiresAt string `json:"expiresAt"`
	// ClusterRegistrationName is the name of the ClusterRegistration CR created.
	ClusterRegistrationName string `json:"clusterRegistrationName"`
}

// Server handles cluster self-registration requests.
type Server struct {
	Client client.Client
	// TLSConfig optionally enables mTLS on the registration server.
	// If nil, the server relies on the Kubernetes API server's TLS (run behind a Service).
	TLSConfig *tls.Config
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := log.FromContext(r.Context())

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/register" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ClusterName == "" || req.Token == "" {
		http.Error(w, "clusterName and token are required", http.StatusBadRequest)
		return
	}

	// Hash the raw token — this is the only form we compare or store.
	hash := sha256.Sum256([]byte(req.Token))
	tokenHash := hex.EncodeToString(hash[:])

	// log.Info intentionally does NOT log the raw token.
	log.Info("registration request", "cluster", req.ClusterName, "tokenHash", tokenHash[:16]+"...")

	resp, err := s.processRegistration(r.Context(), req.ClusterName, tokenHash)
	if err != nil {
		switch e := err.(type) {
		case *registrationError:
			log.Info("registration rejected", "cluster", req.ClusterName, "reason", e.reason)
			http.Error(w, e.reason, e.status)
		default:
			log.Error(err, "registration error", "cluster", req.ClusterName)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
	log.Info("registration complete", "cluster", req.ClusterName)
}

func (s *Server) processRegistration(ctx context.Context, clusterName, tokenHash string) (*RegisterResponse, error) {
	bt, err := s.findBootstrapToken(ctx, clusterName, tokenHash)
	if err != nil {
		return nil, err
	}

	if err := s.ensureClusterRegistration(ctx, clusterName, bt.Spec.Labels); err != nil {
		return nil, fmt.Errorf("ensure ClusterRegistration: %w", err)
	}

	saName := "kapro-cluster-" + clusterName

	if err := s.ensureServiceAccount(ctx, saName, clusterName); err != nil {
		return nil, fmt.Errorf("ensure ServiceAccount: %w", err)
	}

	roleName := "kapro:cluster-controller:" + clusterName
	if err := s.ensureClusterRole(ctx, roleName, clusterName); err != nil {
		return nil, fmt.Errorf("ensure ClusterRole: %w", err)
	}
	if err := s.ensureClusterRoleBinding(ctx, roleName, saName); err != nil {
		return nil, fmt.Errorf("ensure ClusterRoleBinding: %w", err)
	}

	saToken, err := s.issueToken(ctx, saName)
	if err != nil {
		return nil, fmt.Errorf("issue SA token: %w", err)
	}

	// Mark token used BEFORE returning credentials — prevents race on concurrent requests.
	if err := s.markTokenUsed(ctx, bt, saName); err != nil {
		return nil, fmt.Errorf("mark token used: %w", err)
	}

	expiresAt := time.Now().Add(time.Duration(TokenExpirySeconds) * time.Second).UTC().Format(time.RFC3339)

	return &RegisterResponse{
		SAToken:                 saToken,
		ExpiresAt:               expiresAt,
		ClusterRegistrationName: clusterName,
	}, nil
}

// findBootstrapToken finds a valid, unused, non-expired BootstrapToken for the given cluster and hash.
func (s *Server) findBootstrapToken(ctx context.Context, clusterName, tokenHash string) (*kaprov1alpha1.BootstrapToken, error) {
	var list kaprov1alpha1.BootstrapTokenList
	if err := s.Client.List(ctx, &list, client.InNamespace(KaproSystemNamespace)); err != nil {
		return nil, fmt.Errorf("list BootstrapTokens: %w", err)
	}

	for i := range list.Items {
		bt := &list.Items[i]
		if bt.Spec.ClusterName != clusterName {
			continue
		}
		if bt.Status.Used {
			return nil, &registrationError{status: http.StatusConflict, reason: "token already used"}
		}
		if time.Now().After(bt.Spec.ExpiresAt.Time) {
			return nil, &registrationError{status: http.StatusUnauthorized, reason: "token expired"}
		}
		// Constant-time comparison to prevent timing attacks.
		if !secureCompare(bt.Spec.TokenHash, tokenHash) {
			return nil, &registrationError{status: http.StatusUnauthorized, reason: "invalid token"}
		}
		return bt, nil
	}
	return nil, &registrationError{status: http.StatusNotFound, reason: "no matching bootstrap token found"}
}

// secureCompare performs a constant-time string comparison to prevent timing attacks.
func secureCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func (s *Server) ensureClusterRegistration(ctx context.Context, clusterName string, labels map[string]string) error {
	reg := &kaprov1alpha1.ClusterRegistration{}
	err := s.Client.Get(ctx, types.NamespacedName{Name: clusterName}, reg)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	reg = &kaprov1alpha1.ClusterRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:   clusterName,
			Labels: labels,
		},
	}
	return s.Client.Create(ctx, reg)
}

func (s *Server) ensureServiceAccount(ctx context.Context, saName, clusterName string) error {
	sa := &corev1.ServiceAccount{}
	err := s.Client.Get(ctx, types.NamespacedName{Namespace: KaproSystemNamespace, Name: saName}, sa)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	sa = &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: KaproSystemNamespace,
			Labels:    map[string]string{"kapro.io/cluster": clusterName},
		},
	}
	return s.Client.Create(ctx, sa)
}

func (s *Server) ensureClusterRole(ctx context.Context, roleName, clusterName string) error {
	role := &rbacv1.ClusterRole{}
	err := s.Client.Get(ctx, types.NamespacedName{Name: roleName}, role)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	role = &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   roleName,
			Labels: map[string]string{"kapro.io/cluster": clusterName},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"kapro.io"},
				Resources:     []string{"clusterregistrations"},
				ResourceNames: []string{clusterName},
				Verbs:         []string{"get", "update", "patch"},
			},
			{
				APIGroups:     []string{"kapro.io"},
				Resources:     []string{"clusterregistrations/status"},
				ResourceNames: []string{clusterName},
				Verbs:         []string{"update", "patch"},
			},
		},
	}
	return s.Client.Create(ctx, role)
}

func (s *Server) ensureClusterRoleBinding(ctx context.Context, roleName, saName string) error {
	binding := &rbacv1.ClusterRoleBinding{}
	err := s.Client.Get(ctx, types.NamespacedName{Name: roleName}, binding)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	binding = &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: roleName},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: roleName},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: saName, Namespace: KaproSystemNamespace},
		},
	}
	return s.Client.Create(ctx, binding)
}

func (s *Server) issueToken(ctx context.Context, saName string) (string, error) {
	expiry := TokenExpirySeconds
	treq := &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{ExpirationSeconds: &expiry},
	}
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: KaproSystemNamespace},
	}
	if err := s.Client.SubResource("token").Create(ctx, sa, treq); err != nil {
		return "", err
	}
	return treq.Status.Token, nil
}

func (s *Server) markTokenUsed(ctx context.Context, bt *kaprov1alpha1.BootstrapToken, saName string) error {
	now := metav1.Now()
	bt.Status.Used = true
	bt.Status.UsedAt = &now
	bt.Status.IssuedCredentialFor = saName
	return s.Client.Status().Update(ctx, bt)
}

// registrationError is a typed error with HTTP status and a reason string safe to return to clients.
type registrationError struct {
	status int
	reason string
}

func (e *registrationError) Error() string {
	return fmt.Sprintf("registration error %d: %s", e.status, e.reason)
}

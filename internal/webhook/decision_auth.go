package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	authnclient "k8s.io/client-go/kubernetes/typed/authentication/v1"
	authzclient "k8s.io/client-go/kubernetes/typed/authorization/v1"
)

// DecisionAuthenticator authenticates Decision API bearer tokens.
type DecisionAuthenticator interface {
	Authenticate(ctx context.Context, token string) (*authnv1.UserInfo, error)
}

// DecisionAuthorizer authorizes authenticated Decision API callers.
type DecisionAuthorizer interface {
	Authorize(ctx context.Context, user authnv1.UserInfo, attrs authzv1.ResourceAttributes) error
}

// KubernetesDecisionAuthenticator validates ServiceAccount and user bearer
// tokens through the Kubernetes TokenReview API.
type KubernetesDecisionAuthenticator struct {
	Client authnclient.AuthenticationV1Interface
}

// Authenticate validates token and returns the Kubernetes user identity.
func (a KubernetesDecisionAuthenticator) Authenticate(ctx context.Context, token string) (*authnv1.UserInfo, error) {
	if a.Client == nil {
		return nil, errors.New("tokenreview client is not configured")
	}
	review, err := a.Client.TokenReviews().Create(ctx, &authnv1.TokenReview{
		Spec: authnv1.TokenReviewSpec{Token: token},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("tokenreview failed: %w", err)
	}
	if !review.Status.Authenticated {
		if review.Status.Error != "" {
			return nil, errors.New(review.Status.Error)
		}
		return nil, errors.New("token is not authenticated")
	}
	return &review.Status.User, nil
}

// KubernetesDecisionAuthorizer checks each requested action through
// Kubernetes SubjectAccessReview.
type KubernetesDecisionAuthorizer struct {
	Client authzclient.AuthorizationV1Interface
}

// Authorize returns nil only when Kubernetes RBAC allows attrs for user.
func (a KubernetesDecisionAuthorizer) Authorize(ctx context.Context, user authnv1.UserInfo, attrs authzv1.ResourceAttributes) error {
	if a.Client == nil {
		return errors.New("subjectaccessreview client is not configured")
	}
	extra := map[string]authzv1.ExtraValue{}
	for key, values := range user.Extra {
		extra[key] = authzv1.ExtraValue(values)
	}
	review, err := a.Client.SubjectAccessReviews().Create(ctx, &authzv1.SubjectAccessReview{
		Spec: authzv1.SubjectAccessReviewSpec{
			User:               user.Username,
			Groups:             user.Groups,
			Extra:              extra,
			ResourceAttributes: &attrs,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("subjectaccessreview failed: %w", err)
	}
	if review.Status.Allowed {
		return nil
	}
	if review.Status.Reason != "" {
		return errors.New(review.Status.Reason)
	}
	return fmt.Errorf("RBAC denied %s %s/%s", attrs.Verb, attrs.Group, attrs.Resource)
}

func bearerToken(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	return token, token != ""
}

func (s *Server) requireDecisionAccess(ctx context.Context, w http.ResponseWriter, r *http.Request, attrs ...authzv1.ResourceAttributes) (*authnv1.UserInfo, bool) {
	if s.DecisionAuthenticator == nil || s.DecisionAuthorizer == nil {
		http.Error(w, "Decision API authentication is not configured", http.StatusServiceUnavailable)
		return nil, false
	}
	token, ok := bearerToken(r)
	if !ok {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return nil, false
	}
	user, err := s.DecisionAuthenticator.Authenticate(ctx, token)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	if err := s.authorizeDecisionUser(ctx, *user, attrs...); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	return user, true
}

func (s *Server) authorizeDecisionUser(ctx context.Context, user authnv1.UserInfo, attrs ...authzv1.ResourceAttributes) error {
	if s.DecisionAuthorizer == nil {
		return errors.New("decision API authorization is not configured")
	}
	for _, attr := range attrs {
		if err := s.DecisionAuthorizer.Authorize(ctx, user, attr); err != nil {
			return err
		}
	}
	return nil
}

func kaproAttrs(verb, resource, name string) authzv1.ResourceAttributes {
	return authzv1.ResourceAttributes{
		Group:    "kapro.io",
		Verb:     verb,
		Resource: resource,
		Name:     name,
	}
}

func kaproSubresourceAttrs(verb, resource, subresource, name string) authzv1.ResourceAttributes {
	return authzv1.ResourceAttributes{
		Group:       "kapro.io",
		Verb:        verb,
		Resource:    resource,
		Subresource: subresource,
		Name:        name,
	}
}

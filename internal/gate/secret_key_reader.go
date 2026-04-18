package gate

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ClientSecretKeyReader implements SecretKeyReader using a controller-runtime client.
// It reads a named key from a Kubernetes Secret — used to fetch cosign public keys
// for the static-key verification path.
type ClientSecretKeyReader struct {
	Client client.Client
}

var _ SecretKeyReader = &ClientSecretKeyReader{}

// ReadKey fetches the data at secretKey from Secret namespace/name.
func (r *ClientSecretKeyReader) ReadKey(ctx context.Context, namespace, name, secretKey string) ([]byte, error) {
	var secret corev1.Secret
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &secret); err != nil {
		return nil, fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
	}
	data, ok := secret.Data[secretKey]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s has no key %q", namespace, name, secretKey)
	}
	return data, nil
}

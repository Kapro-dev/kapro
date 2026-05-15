// Package secret provides self-bootstrapping HMAC key management.
//
// Uses the same operational pattern as cert-manager's dynamic certificate authority:
//   - On startup: read Secret → if missing, generate + create
//   - Informer watch: if Secret is deleted, recreate immediately
//   - Concurrent replicas: IsAlreadyExists → read winner's key
//   - Mutex-protected: no concurrent regeneration
package secret

import (
	"context"
	crypto_rand "crypto/rand"
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

const (
	SecretName = "kapro-approval-hmac"
	dataKey    = "hmac-key"
	keySize    = 32
)

// HMACKeyProvider manages a self-bootstrapping HMAC key stored as a K8s Secret.
// If the Secret is deleted, it recreates it with the same in-memory key.
// If the operator restarts, it reads the existing Secret.
type HMACKeyProvider struct {
	Namespace string
	Log       logr.Logger

	mu        sync.Mutex
	key       []byte
	clientset kubernetes.Interface
	cancel    context.CancelFunc
}

// Bootstrap loads or generates the HMAC key and starts the informer watch.
// Call this once at startup before the manager starts.
func (p *HMACKeyProvider) Bootstrap(cfg *rest.Config) ([]byte, error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create clientset: %w", err)
	}
	p.clientset = clientset

	// Try to load existing Secret.
	key, err := p.loadOrCreate(context.Background())
	if err != nil {
		return nil, err
	}
	p.key = key

	// Start informer watch — recreates Secret if deleted.
	p.startWatch()

	return key, nil
}

// Key returns the current HMAC key.
func (p *HMACKeyProvider) Key() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.key
}

// Stop shuts down the informer watch.
func (p *HMACKeyProvider) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
}

// loadOrCreate reads the Secret, or generates a new key and creates the Secret.
func (p *HMACKeyProvider) loadOrCreate(ctx context.Context) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Try to read existing.
	existing, err := p.clientset.CoreV1().Secrets(p.Namespace).Get(ctx, SecretName, metav1.GetOptions{})
	if err == nil {
		if v, ok := existing.Data[dataKey]; ok && len(v) > 0 {
			p.Log.Info("loaded approval secret from K8s Secret", "secret", SecretName)
			return v, nil
		}
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read approval secret: %w", err)
	}

	// Generate new key.
	key := make([]byte, keySize)
	if _, err := crypto_rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate random key: %w", err)
	}

	// Create Secret.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SecretName,
			Namespace: p.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kapro-operator",
				"app.kubernetes.io/component":  "approval-hmac",
			},
		},
		Data: map[string][]byte{
			dataKey: key,
		},
	}

	if _, err := p.clientset.CoreV1().Secrets(p.Namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Another replica created it — read theirs.
			existing, getErr := p.clientset.CoreV1().Secrets(p.Namespace).Get(ctx, SecretName, metav1.GetOptions{})
			if getErr == nil {
				if v, ok := existing.Data[dataKey]; ok && len(v) > 0 {
					p.Log.Info("loaded approval secret created by another replica", "secret", SecretName)
					return v, nil
				}
			}
			return nil, fmt.Errorf("read secret after AlreadyExists: %w", getErr)
		}
		return nil, fmt.Errorf("create approval secret: %w", err)
	}

	p.Log.Info("auto-generated approval HMAC secret", "secret", SecretName, "namespace", p.Namespace)
	return key, nil
}

// startWatch sets up an informer that recreates the Secret if it's deleted.
// Same pattern as cert-manager's DynamicAuthority handleDelete.
func (p *HMACKeyProvider) startWatch() {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	factory := informers.NewSharedInformerFactoryWithOptions(
		p.clientset, 0,
		informers.WithNamespace(p.Namespace),
	)

	secretInformer := factory.Core().V1().Secrets().Informer()
	_, _ = secretInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			secret, ok := obj.(*corev1.Secret)
			if !ok {
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					return
				}
				secret, ok = tombstone.Obj.(*corev1.Secret)
				if !ok {
					return
				}
			}
			if secret.Name != SecretName {
				return
			}
			p.Log.Info("approval HMAC secret deleted, recreating", "secret", SecretName)
			p.ensureSecret()
		},
	})

	go factory.Start(ctx.Done())
}

// ensureSecret recreates the Secret using the in-memory key.
func (p *HMACKeyProvider) ensureSecret() {
	p.mu.Lock()
	key := p.key
	p.mu.Unlock()

	if len(key) == 0 {
		return
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SecretName,
			Namespace: p.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kapro-operator",
				"app.kubernetes.io/component":  "approval-hmac",
			},
		},
		Data: map[string][]byte{
			dataKey: key,
		},
	}

	_, err := p.clientset.CoreV1().Secrets(p.Namespace).Create(context.Background(), secret, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return // another replica recreated it, fine
		}
		p.Log.Error(err, "failed to recreate approval secret after deletion")
		return
	}
	p.Log.Info("recreated approval HMAC secret after deletion", "secret", SecretName)
}

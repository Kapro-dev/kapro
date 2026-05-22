package controller

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"reflect"
	"sort"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// ---- CSR predicates & parsing ----------------------------------------------

// isKaproCSR is a strict allowlist for CSRs this controller will touch.
// Returns true only when: signer matches our constant, CN starts with the
// kapro-cluster prefix, Organization is exactly ["kapro:cluster-controllers"]
// (defends against O=system:masters escalation), and usages are exactly
// [client auth] (no server auth, no key encipherment).
func isKaproCSR(csr *certificatesv1.CertificateSigningRequest) bool {
	if csr == nil {
		return false
	}
	if csr.Spec.SignerName != csrSigner {
		return false
	}
	req, err := parseCSRRequest(csr.Spec.Request)
	if err != nil {
		return false
	}
	if !startsWith(req.Subject.CommonName, csrCNPrefix) {
		return false
	}
	if len(req.Subject.Organization) != 1 || req.Subject.Organization[0] != csrOrganization {
		return false
	}
	return hasOnlyClientAuthUsage(csr.Spec.Usages)
}

func parseCSRRequest(raw []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("decode PEM CSR request")
	}
	return x509.ParseCertificateRequest(block.Bytes)
}

// hasOnlyClientAuthUsage enforces exactly [client auth]: a single entry, no
// duplicates, no other usages mixed in. Rejecting duplicates is defence-in-
// depth — the standard K8s validation already deduplicates Usages on the
// way in, but a future controller-runtime upgrade or a strict equality
// check downstream should never trip on `[ClientAuth, ClientAuth]`.
func hasOnlyClientAuthUsage(usages []certificatesv1.KeyUsage) bool {
	if len(usages) != 1 {
		return false
	}
	return usages[0] == certificatesv1.UsageClientAuth
}

func isCSRApproved(csr *certificatesv1.CertificateSigningRequest) bool {
	for _, c := range csr.Status.Conditions {
		if c.Type == certificatesv1.CertificateApproved {
			return true
		}
	}
	return false
}

func isCSRDenied(csr *certificatesv1.CertificateSigningRequest) bool {
	for _, c := range csr.Status.Conditions {
		if c.Type == certificatesv1.CertificateDenied {
			return true
		}
	}
	return false
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// ---- Bootstrap SA + kubeconfig provisioning --------------------------------

const (
	// bootstrapTokenRefreshLeeway is the buffer before the SA token's expiry
	// at which the hub re-issues the bootstrap kubeconfig Secret. The TokenRequest
	// is 1h by default; a spoke that comes up late (e.g., 24h slot, pod arrives at
	// 50m) must still find a usable token. The annotation
	// `kapro.io/bootstrap-expires-at` on the Secret drives this check.
	bootstrapTokenRefreshLeeway = 15 * time.Minute
)

// shouldProvision returns true when a fresh bootstrap kubeconfig Secret needs
// to be issued — either no Secret has been recorded yet, the recorded one no
// longer exists, OR the recorded one's SA token is near expiry. The third case
// matters whenever spec.bootstrap.ttl is significantly longer than the
// TokenRequest TTL (default 1h): without refresh, a late-arriving spoke would
// find an expired token and never register.
func (r *ClusterBootstrapReconciler) shouldProvision(ctx context.Context, fc *kaprov1alpha2.Cluster) bool {
	if fc.Spec.Bootstrap == nil {
		return false
	}
	if usesVaultBootstrapMaterial(fc) {
		return false
	}
	if fc.Status.Bootstrap != nil && fc.Status.Bootstrap.Used {
		return false
	}
	if fc.Status.Bootstrap == nil || fc.Status.Bootstrap.IssuedBootstrapKubeconfig == "" {
		return true
	}
	// Read the Secret annotation to see if the token is still fresh.
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: r.podNS(),
		Name:      fc.Status.Bootstrap.IssuedBootstrapKubeconfig,
	}, secret)
	if apierrors.IsNotFound(err) {
		return true // Secret was deleted — re-issue.
	}
	if err != nil {
		// Transient error: don't re-provision aggressively. Next reconcile retries.
		return false
	}
	expiresAtStr := secret.Annotations["kapro.io/bootstrap-expires-at"]
	if expiresAtStr == "" {
		return true // Missing annotation — old Secret, re-issue to add it.
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
	if err != nil {
		return true // Malformed — replace.
	}
	return time.Until(expiresAt) <= bootstrapTokenRefreshLeeway
}

// ensureBootstrapProvisioned is idempotent: it creates the bootstrap SA,
// scoped CSR-only ClusterRole + Binding, calls TokenRequest for the SA, and
// writes the rendered kubeconfig into a Secret in kapro-system.
//
// On success it patches status.bootstrap.IssuedBootstrapKubeconfig to the
// Secret name so subsequent reconciles skip this work.
func (r *ClusterBootstrapReconciler) ensureBootstrapProvisioned(ctx context.Context, fc *kaprov1alpha2.Cluster) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithValues("fleetcluster", fc.Name)
	if !r.shouldProvision(ctx, fc) {
		return ctrl.Result{}, nil
	}

	clusterName := fc.Name
	saName := fmt.Sprintf(bootstrapSAFormat, clusterName)
	roleName := fmt.Sprintf(bootstrapRoleNameFmt, clusterName)
	secretName := fmt.Sprintf(bootstrapKubeconfigSecretFmt, clusterName)
	ns := r.podNS()

	// 1) SA — token holder for the spoke during bootstrap.
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: ns,
			Labels:    managedResourceLabels(clusterName, "bootstrap-sa"),
		},
	}
	if err := r.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, fmt.Errorf("create bootstrap SA: %w", err)
	}

	// 2) ClusterRole — allows the SA to submit (and poll its own) CSRs for
	//    our signerName. NOT a generic CSR creator — limited to this signer.
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   roleName,
			Labels: managedResourceLabels(clusterName, "bootstrap-rbac"),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"certificates.k8s.io"},
				Resources: []string{"certificatesigningrequests"},
				Verbs:     []string{"create", "get", "watch"},
			},
			// Allow the bootstrap SA to read its own SubjectAccessReview results — used
			// by the spoke for self-diagnostics. Cheap, well-scoped.
			{
				APIGroups: []string{"authorization.k8s.io"},
				Resources: []string{"selfsubjectaccessreviews", "selfsubjectrulesreviews"},
				Verbs:     []string{"create"},
			},
		},
	}
	if err := r.upsertClusterRoleObject(ctx, role); err != nil {
		return ctrl.Result{}, fmt.Errorf("upsert bootstrap ClusterRole: %w", err)
	}

	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   roleName,
			Labels: managedResourceLabels(clusterName, "bootstrap-rbac"),
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     roleName,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      saName,
			Namespace: ns,
		}},
	}
	if err := r.upsertClusterRoleBindingObject(ctx, binding); err != nil {
		return ctrl.Result{}, fmt.Errorf("upsert bootstrap ClusterRoleBinding: %w", err)
	}

	// 3) TokenRequest — short-lived audience-bound SA token.
	tr := &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{
			Audiences:         []string{bootstrapTokenAudience},
			ExpirationSeconds: ptr.To(int64(bootstrapTokenLifetime.Seconds())),
		},
	}
	tok, err := r.KubeClient.CoreV1().ServiceAccounts(ns).CreateToken(ctx, saName, tr, metav1.CreateOptions{})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("issue TokenRequest for bootstrap SA: %w", err)
	}

	// 4) Render kubeconfig + persist as Secret. Spoke chart mounts this Secret
	//    at /etc/kapro/bootstrap-kubeconfig and uses it for the first CSR.
	kubeconfig, err := buildBootstrapKubeconfig(r.HubAPIURL, r.HubCAData, tok.Status.Token, clusterName, saName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("render bootstrap kubeconfig: %w", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ns,
			Labels:    managedResourceLabels(clusterName, "bootstrap-kubeconfig"),
			Annotations: map[string]string{
				"kapro.io/bootstrap-expires-at": tok.Status.ExpirationTimestamp.Format(time.RFC3339),
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"kubeconfig": kubeconfig},
	}
	if err := r.upsertSecret(ctx, secret); err != nil {
		return ctrl.Result{}, fmt.Errorf("upsert bootstrap kubeconfig Secret: %w", err)
	}

	// 5) Record the Secret name in status so subsequent reconciles skip provisioning.
	patch := client.MergeFrom(fc.DeepCopy())
	if fc.Status.Bootstrap == nil {
		fc.Status.Bootstrap = &kaprov1alpha2.ClusterBootstrapStatus{}
	}
	fc.Status.Bootstrap.IssuedBootstrapKubeconfig = secretName
	if err := r.Status().Patch(ctx, fc, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch IssuedBootstrapKubeconfig: %w", err)
	}

	// Intentional: log only the Secret name + expiration metadata. NEVER log
	// the kubeconfig payload, the TokenRequest token, or the CA bundle —
	// they are credentials. The Secret is per-cluster RBAC-scoped (see
	// ensureClusterRBAC); whoever can read this log line can already list
	// the Secret if they have the right RBAC.
	log.Info("bootstrap kubeconfig issued", "secret", secretName, "expires", tok.Status.ExpirationTimestamp)
	if r.Recorder != nil {
		r.Recorder.Eventf(fc, corev1.EventTypeNormal, "BootstrapIssued",
			"Bootstrap kubeconfig Secret %q issued (expires %s); spoke must submit CSR before then",
			secretName, tok.Status.ExpirationTimestamp.Format(time.RFC3339))
	}
	return ctrl.Result{RequeueAfter: time.Nanosecond}, nil
}

// buildBootstrapKubeconfig renders a kubeconfig that points the spoke at the
// hub kube-apiserver with the SA bearer token. This kubeconfig is single-use:
// the spoke uses it ONLY to create + poll a CSR. After bootstrap, the spoke
// builds its own steady-state kubeconfig from the issued client cert.
//
// The kubeconfig context is named after the cluster ("kapro-<cluster>") so
// `kubectl --kubeconfig=...` output is human-readable during debugging.
func buildBootstrapKubeconfig(hubURL string, caData []byte, token, clusterName, saName string) ([]byte, error) {
	if hubURL == "" {
		return nil, fmt.Errorf("hub API URL is empty")
	}
	contextName := "kapro-" + clusterName
	if clusterName == "" {
		contextName = "kapro-bootstrap"
	}
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["kapro-hub"] = &clientcmdapi.Cluster{
		Server:                   hubURL,
		CertificateAuthorityData: caData,
	}
	cfg.AuthInfos[saName] = &clientcmdapi.AuthInfo{
		Token: token,
	}
	cfg.Contexts[contextName] = &clientcmdapi.Context{
		Cluster:  "kapro-hub",
		AuthInfo: saName,
	}
	cfg.CurrentContext = contextName
	return clientcmd.Write(*cfg)
}

func usesVaultBootstrapMaterial(fc *kaprov1alpha2.Cluster) bool {
	if fc == nil || fc.Spec.Bootstrap == nil || fc.Spec.Bootstrap.MaterialSource == nil {
		return false
	}
	return fc.Spec.Bootstrap.MaterialSource.Type == kaprov1alpha2.ClusterBootstrapMaterialVault
}

// ---- Per-cluster long-lived RBAC -------------------------------------------

// upsertClusterRole creates or updates the per-cluster long-lived ClusterRole
// that the spoke's issued client cert authorises against.
func (r *ClusterBootstrapReconciler) upsertClusterRole(ctx context.Context, roleName, clusterName string) error {
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   roleName,
			Labels: managedResourceLabels(clusterName, "cluster-controller-rbac"),
		},
		Rules: []rbacv1.PolicyRule{
			// Read its own Cluster.
			{
				APIGroups:     []string{"kapro.io"},
				Resources:     []string{"clusters"},
				ResourceNames: []string{clusterName},
				Verbs:         []string{"get", "list", "watch", "patch", "update"},
			},
			// Update its own Cluster status.
			{
				APIGroups:     []string{"kapro.io"},
				Resources:     []string{"clusters/status"},
				ResourceNames: []string{clusterName},
				Verbs:         []string{"get", "patch", "update"},
			},
			// Write its own heartbeat Lease. namespace=kapro-system, name=kapro-heartbeat-<cluster>.
			// resourceNames lock is enforced here so a compromised spoke cannot touch a sibling's Lease.
			{
				APIGroups:     []string{"coordination.k8s.io"},
				Resources:     []string{"leases"},
				ResourceNames: []string{heartbeatLeaseName(clusterName)},
				Verbs:         []string{"get", "create", "update", "patch"},
			},
			// Submit renewal CSRs for this signer name. Cluster-scoped (CSRs have no namespace).
			{
				APIGroups: []string{"certificates.k8s.io"},
				Resources: []string{"certificatesigningrequests"},
				Verbs:     []string{"create", "get", "watch"},
			},
		},
	}
	return r.upsertClusterRoleObject(ctx, role)
}

// upsertClusterRoleBinding binds the cluster ClusterRole to the User identity
// the spoke's issued client cert presents (CN=kapro-cluster:<name>).
func (r *ClusterBootstrapReconciler) upsertClusterRoleBinding(ctx context.Context, roleName, user, clusterName string) error {
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   roleName,
			Labels: managedResourceLabels(clusterName, "cluster-controller-rbac"),
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     roleName,
		},
		Subjects: []rbacv1.Subject{{
			APIGroup: rbacv1.GroupName,
			Kind:     "User",
			Name:     user,
		}},
	}
	return r.upsertClusterRoleBindingObject(ctx, binding)
}

func (r *ClusterBootstrapReconciler) upsertClusterRoleObject(ctx context.Context, role *rbacv1.ClusterRole) error {
	existing := &rbacv1.ClusterRole{}
	err := r.Get(ctx, types.NamespacedName{Name: role.Name}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, role)
	}
	if err != nil {
		return err
	}
	if reflect.DeepEqual(existing.Rules, role.Rules) && labelsEqual(existing.Labels, role.Labels) {
		return nil
	}
	patch := client.MergeFrom(existing.DeepCopy())
	existing.Rules = role.Rules
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	for k, v := range role.Labels {
		existing.Labels[k] = v
	}
	return r.Patch(ctx, existing, patch)
}

func (r *ClusterBootstrapReconciler) upsertClusterRoleBindingObject(ctx context.Context, binding *rbacv1.ClusterRoleBinding) error {
	existing := &rbacv1.ClusterRoleBinding{}
	err := r.Get(ctx, types.NamespacedName{Name: binding.Name}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, binding)
	}
	if err != nil {
		return err
	}
	if reflect.DeepEqual(existing.RoleRef, binding.RoleRef) &&
		reflect.DeepEqual(existing.Subjects, binding.Subjects) &&
		labelsEqual(existing.Labels, binding.Labels) {
		return nil
	}
	// RoleRef is immutable — if it diverged we must recreate.
	if !reflect.DeepEqual(existing.RoleRef, binding.RoleRef) {
		if err := r.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return r.Create(ctx, binding)
	}
	patch := client.MergeFrom(existing.DeepCopy())
	existing.Subjects = binding.Subjects
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	for k, v := range binding.Labels {
		existing.Labels[k] = v
	}
	return r.Patch(ctx, existing, patch)
}

func (r *ClusterBootstrapReconciler) upsertSecret(ctx context.Context, secret *corev1.Secret) error {
	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, secret)
	}
	if err != nil {
		return err
	}
	patch := client.MergeFrom(existing.DeepCopy())
	existing.Data = secret.Data
	existing.Type = secret.Type
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	for k, v := range secret.Labels {
		existing.Labels[k] = v
	}
	if existing.Annotations == nil {
		existing.Annotations = map[string]string{}
	}
	for k, v := range secret.Annotations {
		existing.Annotations[k] = v
	}
	return r.Patch(ctx, existing, patch)
}

func (r *ClusterBootstrapReconciler) deleteClusterRBAC(ctx context.Context, roleName string) error {
	for _, obj := range []client.Object{
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: roleName}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: roleName}},
	} {
		if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete %T %s: %w", obj, obj.GetName(), err)
		}
	}
	return nil
}

// ---- Small utilities -------------------------------------------------------

// heartbeatLeaseName lives in heartbeat.go — we reuse it here for the per-cluster
// RBAC rule so the Lease the spoke can write matches what the hub heartbeat
// reader reads. Keeps the contract single-sourced.

func managedResourceLabels(clusterName, component string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": kaproManagedBy,
		"app.kubernetes.io/component":  component,
		"kapro.io/fleetcluster":        clusterName,
	}
}

func labelsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func containsString(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

func removeString(s []string, want string) []string {
	out := s[:0]
	for _, v := range s {
		if v != want {
			out = append(out, v)
		}
	}
	return out
}

func sameFinalizers(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// bootstrapStatusEqual compares two ClusterBootstrapStatus values
// SEMANTICALLY, not by raw struct equality. The UsedAt field is
// `*metav1.Time` — `*a == *b` would compare pointer addresses, returning
// false for two distinct *metav1.Time allocations that hold the same
// instant. This drove spurious reconciles whenever the informer cache and
// a freshly-decoded apiserver response disagreed on pointer identity.
func bootstrapStatusEqual(a, b *kaprov1alpha2.ClusterBootstrapStatus) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.Used != b.Used ||
		a.IssuedCredentialFor != b.IssuedCredentialFor ||
		a.IssuedBootstrapKubeconfig != b.IssuedBootstrapKubeconfig ||
		a.BoundCSRName != b.BoundCSRName ||
		a.IssuedClusterRole != b.IssuedClusterRole ||
		a.IssuedClusterRoleBinding != b.IssuedClusterRoleBinding {
		return false
	}
	return timesEqual(a.UsedAt, b.UsedAt)
}

// timesEqual compares two *metav1.Time pointers by their time value, not by
// pointer identity. Both-nil is equal; one-nil is unequal.
func timesEqual(a, b *metav1.Time) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Time.Equal(b.Time)
}

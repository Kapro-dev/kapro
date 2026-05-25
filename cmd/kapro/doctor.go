package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/internal/cli"
)

const (
	doctorStatusPass = "pass"
	doctorStatusWarn = "warn"
	doctorStatusFail = "fail"
	doctorStatusSkip = "skip"
)

type doctorOptions struct {
	Kubeconfig string
	Namespace  string
	Deployment string
}

type doctorReport struct {
	Overall string          `json:"overall"`
	Checks  []doctorFinding `json:"checks"`
}

type doctorFinding struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Message string   `json:"message"`
	Details []string `json:"details,omitempty"`
}

type doctorFailedError struct {
	failures int
}

func (e doctorFailedError) Error() string {
	if e.failures == 1 {
		return "kapro doctor found 1 failing required check"
	}
	return fmt.Sprintf("kapro doctor found %d failing required checks", e.failures)
}

type sarChecker func(ctx context.Context, user string, attrs authv1.ResourceAttributes) (bool, string, error)

func newDoctorCmd() *cobra.Command {
	opts := doctorOptions{Namespace: "kapro-system"}
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run read-only preflight checks for a Kapro hub install",
		Long: `Run preflight checks for the current Kapro hub install.

The report checks CRDs, operator readiness, operator RBAC, admission webhook
enforcement, conversion webhook configuration, and referenced pull secrets.

Exit code: 0 when all required checks pass; 1 when any required check fails or
the command cannot complete. WARN and SKIP findings do not fail the command.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.Kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", opts.Namespace, "Namespace where the Kapro operator is installed")
	cmd.Flags().StringVar(&opts.Deployment, "deployment", "", "Operator Deployment name; auto-detected when unset")
	return cmd
}

func runDoctor(ctx context.Context, opts doctorOptions) error {
	c, err := buildClient(opts.Kubeconfig)
	if err != nil {
		return err
	}
	return runDoctorWithClient(ctx, c, opts, defaultSARChecker(c))
}

func runDoctorWithClient(ctx context.Context, c client.Client, opts doctorOptions, sar sarChecker) error {
	report := collectDoctorReport(ctx, c, opts, sar)
	if cli.IsJSON() {
		if err := cli.JSON(report); err != nil {
			return err
		}
	} else {
		renderDoctorReport(report)
	}
	if failures := countDoctorFailures(report); failures > 0 {
		return doctorFailedError{failures: failures}
	}
	return nil
}

func collectDoctorReport(ctx context.Context, c client.Client, opts doctorOptions, sar sarChecker) doctorReport {
	var findings []doctorFinding
	findings = append(findings, checkKaproCRDs(ctx, c))
	deployment, deployFinding := checkOperatorDeployment(ctx, c, opts)
	findings = append(findings, deployFinding)
	findings = append(findings, checkOperatorRBAC(ctx, sar, opts.Namespace, deployment))
	findings = append(findings, checkValidatingWebhook(ctx, c))
	findings = append(findings, checkConversionWebhookConfig(ctx, c))
	findings = append(findings, checkPullSecrets(ctx, c, opts.Namespace))
	findings = append(findings, checkGitOpsSubstrates(ctx, c))
	report := doctorReport{Overall: doctorStatusPass, Checks: findings}
	if countDoctorFailures(report) > 0 {
		report.Overall = doctorStatusFail
		return report
	}
	for _, f := range findings {
		if f.Status == doctorStatusWarn {
			report.Overall = doctorStatusWarn
			return report
		}
	}
	return report
}

func checkKaproCRDs(ctx context.Context, c client.Client) doctorFinding {
	var missing, problems []string
	for _, name := range expectedKaproCRDs {
		var crd apiextensionsv1.CustomResourceDefinition
		if err := c.Get(ctx, types.NamespacedName{Name: name}, &crd); err != nil {
			if apierrors.IsNotFound(err) {
				missing = append(missing, name)
				continue
			}
			return doctorFinding{Name: "crds", Status: doctorStatusFail, Message: "could not read Kapro CRDs", Details: []string{err.Error()}}
		}
		if !crdEstablished(crd) {
			problems = append(problems, "not established: "+name)
		}
		if !crdNamesAccepted(crd) {
			problems = append(problems, "names not accepted: "+name)
		}
		expectedGroup := expectedCRDGroup(name)
		if crd.Spec.Group != expectedGroup {
			problems = append(problems, fmt.Sprintf("wrong group: %s has %q, want %q", name, crd.Spec.Group, expectedGroup))
		}
		if crd.Spec.Scope != apiextensionsv1.ClusterScoped {
			problems = append(problems, fmt.Sprintf("wrong scope: %s has %q", name, crd.Spec.Scope))
		}
		if !crdServesVersion(crd, "v1alpha1") {
			problems = append(problems, "missing served v1alpha1: "+name)
		}
	}
	if len(missing) > 0 || len(problems) > 0 {
		details := append(prefixDetails("missing: ", missing), problems...)
		return doctorFinding{Name: "crds", Status: doctorStatusFail, Message: "Kapro CRDs are not fully installed", Details: details}
	}
	return doctorFinding{Name: "crds", Status: doctorStatusPass, Message: fmt.Sprintf("%d Kapro CRDs are Established", len(expectedKaproCRDs))}
}

func crdEstablished(crd apiextensionsv1.CustomResourceDefinition) bool {
	for _, cond := range crd.Status.Conditions {
		if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
			return true
		}
	}
	return false
}

func crdNamesAccepted(crd apiextensionsv1.CustomResourceDefinition) bool {
	for _, cond := range crd.Status.Conditions {
		if cond.Type == apiextensionsv1.NamesAccepted && cond.Status == apiextensionsv1.ConditionTrue {
			return true
		}
	}
	return false
}

func crdServesVersion(crd apiextensionsv1.CustomResourceDefinition, version string) bool {
	for _, v := range crd.Spec.Versions {
		if v.Name == version && v.Served {
			return true
		}
	}
	return false
}

func checkOperatorDeployment(ctx context.Context, c client.Client, opts doctorOptions) (*appsv1.Deployment, doctorFinding) {
	deploy, err := findOperatorDeployment(ctx, c, opts.Namespace, opts.Deployment)
	if err != nil {
		return nil, doctorFinding{Name: "operator", Status: doctorStatusFail, Message: "Kapro operator Deployment is not ready", Details: []string{err.Error()}}
	}
	if deploy.Status.ObservedGeneration < deploy.Generation {
		return deploy, doctorFinding{Name: "operator", Status: doctorStatusFail, Message: "Kapro operator Deployment has not observed the latest generation", Details: []string{deploy.Name}}
	}
	if deploy.Spec.Replicas != nil && *deploy.Spec.Replicas > 0 && deploy.Status.ReadyReplicas < *deploy.Spec.Replicas {
		return deploy, doctorFinding{
			Name:    "operator",
			Status:  doctorStatusFail,
			Message: "Kapro operator Deployment has unavailable replicas",
			Details: []string{fmt.Sprintf("%s/%s ready=%d desired=%d", deploy.Namespace, deploy.Name, deploy.Status.ReadyReplicas, *deploy.Spec.Replicas)},
		}
	}
	if !deploymentAvailable(*deploy) {
		return deploy, doctorFinding{Name: "operator", Status: doctorStatusFail, Message: "Kapro operator Deployment is not Available", Details: []string{deploy.Name}}
	}
	return deploy, doctorFinding{Name: "operator", Status: doctorStatusPass, Message: fmt.Sprintf("Deployment %s/%s is Ready", deploy.Namespace, deploy.Name)}
}

func findOperatorDeployment(ctx context.Context, c client.Client, namespace, explicit string) (*appsv1.Deployment, error) {
	if explicit != "" {
		var deploy appsv1.Deployment
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: explicit}, &deploy); err != nil {
			return nil, err
		}
		return &deploy, nil
	}
	for _, name := range []string{"kapro-kapro-operator", "kapro-operator"} {
		var deploy appsv1.Deployment
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &deploy); err == nil {
			return &deploy, nil
		}
	}
	var list appsv1.DeploymentList
	if err := c.List(ctx, &list, client.InNamespace(namespace), client.MatchingLabels{"app.kubernetes.io/name": "kapro-operator"}); err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		if err := c.List(ctx, &list, client.InNamespace(namespace), client.MatchingLabels{"app": "kapro-operator"}); err != nil {
			return nil, err
		}
	}
	if len(list.Items) == 0 {
		return nil, apierrors.NewNotFound(appsv1.Resource("deployments"), "kapro-operator")
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return &list.Items[0], nil
}

func deploymentAvailable(deploy appsv1.Deployment) bool {
	for _, cond := range deploy.Status.Conditions {
		if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func checkOperatorRBAC(ctx context.Context, sar sarChecker, namespace string, deploy *appsv1.Deployment) doctorFinding {
	if deploy == nil {
		return doctorFinding{Name: "operator-rbac", Status: doctorStatusSkip, Message: "operator Deployment was not found; RBAC check skipped"}
	}
	if sar == nil {
		return doctorFinding{Name: "operator-rbac", Status: doctorStatusSkip, Message: "SubjectAccessReview client unavailable"}
	}
	sa := deploy.Spec.Template.Spec.ServiceAccountName
	if sa == "" {
		sa = "default"
	}
	user := "system:serviceaccount:" + namespace + ":" + sa
	var denied, errorsSeen []string
	for _, access := range requiredOperatorAccess {
		allowed, reason, err := sar(ctx, user, access)
		key := accessKey(access)
		if err != nil {
			if apierrors.IsForbidden(err) {
				return doctorFinding{Name: "operator-rbac", Status: doctorStatusWarn, Message: "cannot create SubjectAccessReview from this kubeconfig", Details: []string{err.Error()}}
			}
			errorsSeen = append(errorsSeen, key+": "+err.Error())
			continue
		}
		if !allowed {
			if reason != "" {
				key += ": " + reason
			}
			denied = append(denied, key)
		}
	}
	if len(errorsSeen) > 0 {
		return doctorFinding{Name: "operator-rbac", Status: doctorStatusWarn, Message: "some SubjectAccessReview checks could not complete", Details: limitDetails(errorsSeen, 12)}
	}
	if len(denied) > 0 {
		return doctorFinding{Name: "operator-rbac", Status: doctorStatusFail, Message: "operator service account is missing required permissions", Details: limitDetails(denied, 20)}
	}
	return doctorFinding{Name: "operator-rbac", Status: doctorStatusPass, Message: "operator service account has required Kapro permissions"}
}

func defaultSARChecker(c client.Client) sarChecker {
	return func(ctx context.Context, user string, attrs authv1.ResourceAttributes) (bool, string, error) {
		sar := &authv1.SubjectAccessReview{
			Spec: authv1.SubjectAccessReviewSpec{
				User:               user,
				ResourceAttributes: &attrs,
			},
		}
		if err := c.Create(ctx, sar); err != nil {
			return false, "", err
		}
		return sar.Status.Allowed, sar.Status.Reason, nil
	}
}

func checkValidatingWebhook(ctx context.Context, c client.Client) doctorFinding {
	configs, err := kaproValidatingWebhookConfigs(ctx, c)
	if err != nil {
		return doctorFinding{Name: "validating-webhook", Status: doctorStatusFail, Message: "could not inspect validating webhook configuration", Details: []string{err.Error()}}
	}
	if len(configs) == 0 {
		return doctorFinding{Name: "validating-webhook", Status: doctorStatusFail, Message: "no Kapro ValidatingWebhookConfiguration found"}
	}
	var serviceErrors []string
	for _, cfg := range configs {
		for _, hook := range cfg.Webhooks {
			if !webhookTouchesKapro(hook.Rules) {
				continue
			}
			if hook.ClientConfig.Service == nil {
				serviceErrors = append(serviceErrors, hook.Name+": no service clientConfig")
				continue
			}
			svcRef := hook.ClientConfig.Service
			var svc corev1.Service
			if err := c.Get(ctx, types.NamespacedName{Namespace: svcRef.Namespace, Name: svcRef.Name}, &svc); err != nil {
				serviceErrors = append(serviceErrors, hook.Name+": "+err.Error())
			}
		}
	}
	if len(serviceErrors) > 0 {
		return doctorFinding{Name: "validating-webhook", Status: doctorStatusFail, Message: "validating webhook service is not reachable through Kubernetes service discovery", Details: limitDetails(serviceErrors, 10)}
	}
	probe := invalidPlanProbe()
	err = c.Create(ctx, probe, client.DryRunAll)
	if err == nil {
		return doctorFinding{Name: "validating-webhook", Status: doctorStatusFail, Message: "validating webhook did not reject an invalid dry-run Plan"}
	}
	if apierrors.IsInvalid(err) {
		return doctorFinding{Name: "validating-webhook", Status: doctorStatusPass, Message: "validating webhook rejected an invalid dry-run Plan"}
	}
	if apierrors.IsForbidden(err) {
		return doctorFinding{Name: "validating-webhook", Status: doctorStatusWarn, Message: "validating webhook is configured, but this kubeconfig cannot dry-run create Plans", Details: []string{err.Error()}}
	}
	return doctorFinding{Name: "validating-webhook", Status: doctorStatusFail, Message: "validating webhook dry-run probe failed", Details: []string{err.Error()}}
}

func kaproValidatingWebhookConfigs(ctx context.Context, c client.Client) ([]admissionv1.ValidatingWebhookConfiguration, error) {
	var list admissionv1.ValidatingWebhookConfigurationList
	if err := c.List(ctx, &list); err != nil {
		return nil, err
	}
	var out []admissionv1.ValidatingWebhookConfiguration
	for _, cfg := range list.Items {
		for _, hook := range cfg.Webhooks {
			if webhookTouchesKapro(hook.Rules) {
				out = append(out, cfg)
				break
			}
		}
	}
	return out, nil
}

func webhookTouchesKapro(rules []admissionv1.RuleWithOperations) bool {
	for _, rule := range rules {
		for _, group := range rule.APIGroups {
			if group == "kapro.io" || group == "runtime.kapro.io" {
				return true
			}
		}
	}
	return false
}

func invalidPlanProbe() *kaprov1alpha1.Plan {
	now := time.Now().UnixNano()
	return &kaprov1alpha1.Plan{
		TypeMeta: metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "Plan"},
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("kapro-doctor-webhook-probe-%x", now),
			Labels: map[string]string{"kapro.io/team": "doctor"},
		},
		Spec: kaprov1alpha1.PlanSpec{
			Stages: []kaprov1alpha1.Stage{
				{Name: "duplicate", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"env": "dev"}}},
				{Name: "duplicate", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}}},
			},
		},
	}
}

func checkConversionWebhookConfig(ctx context.Context, c client.Client) doctorFinding {
	var missing []string
	var webhookCount int
	for _, name := range expectedKaproCRDs {
		var crd apiextensionsv1.CustomResourceDefinition
		if err := c.Get(ctx, types.NamespacedName{Name: name}, &crd); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return doctorFinding{Name: "conversion-webhook", Status: doctorStatusWarn, Message: "could not inspect conversion webhook configuration", Details: []string{err.Error()}}
		}
		if crd.Spec.Conversion == nil || crd.Spec.Conversion.Strategy != apiextensionsv1.WebhookConverter {
			missing = append(missing, name)
			continue
		}
		webhookCount++
		if crd.Spec.Conversion.Webhook == nil || crd.Spec.Conversion.Webhook.ClientConfig == nil {
			return doctorFinding{Name: "conversion-webhook", Status: doctorStatusFail, Message: "conversion webhook strategy is enabled without clientConfig", Details: []string{name}}
		}
	}
	if webhookCount == 0 {
		return doctorFinding{Name: "conversion-webhook", Status: doctorStatusSkip, Message: "CRD conversion webhook strategy is not enabled for this release"}
	}
	if len(missing) > 0 {
		return doctorFinding{Name: "conversion-webhook", Status: doctorStatusFail, Message: "conversion webhook strategy is only partially configured", Details: limitDetails(missing, 13)}
	}
	return doctorFinding{Name: "conversion-webhook", Status: doctorStatusPass, Message: fmt.Sprintf("%d CRDs use webhook conversion", webhookCount)}
}

func checkPullSecrets(ctx context.Context, c client.Client, namespace string) doctorFinding {
	refs, invalid, err := collectReferencedPullSecrets(ctx, c, namespace)
	if err != nil {
		return doctorFinding{Name: "pull-secrets", Status: doctorStatusWarn, Message: "could not inspect pull-secret references", Details: []string{err.Error()}}
	}
	if len(invalid) > 0 {
		return doctorFinding{Name: "pull-secrets", Status: doctorStatusFail, Message: "some private registry pull-secret references are incomplete", Details: invalid}
	}
	if len(refs) == 0 {
		return doctorFinding{Name: "pull-secrets", Status: doctorStatusPass, Message: "no private registry pull-secret references found"}
	}
	var missing []string
	for _, ref := range refs {
		var secret corev1.Secret
		if err := c.Get(ctx, ref.NamespacedName, &secret); err != nil {
			if apierrors.IsNotFound(err) {
				missing = append(missing, ref.String())
				continue
			}
			return doctorFinding{Name: "pull-secrets", Status: doctorStatusWarn, Message: "could not inspect referenced pull secrets", Details: []string{err.Error()}}
		}
	}
	if len(missing) > 0 {
		return doctorFinding{Name: "pull-secrets", Status: doctorStatusFail, Message: "referenced private registry pull secrets are missing", Details: missing}
	}
	return doctorFinding{Name: "pull-secrets", Status: doctorStatusPass, Message: fmt.Sprintf("%d referenced pull secret(s) exist", len(refs))}
}

func checkGitOpsSubstrates(ctx context.Context, c client.Client) doctorFinding {
	var substrates kaprov1alpha1.SubstrateList
	if err := c.List(ctx, &substrates); err != nil && !apierrors.IsNotFound(err) {
		return doctorFinding{Name: "gitops-substrates", Status: doctorStatusWarn, Message: "could not list Kapro Substrate objects", Details: []string{err.Error()}}
	}
	if len(substrates.Items) == 0 {
		return doctorFinding{
			Name:    "gitops-substrates",
			Status:  doctorStatusSkip,
			Message: "no Substrate objects found yet; start with `kapro create direct` or `kapro import argo|flux`",
		}
	}
	var details, namespaceWarnings []string
	for _, substrate := range substrates.Items {
		kind := substrate.Spec.SubstrateKind()
		executionMode := string(substrate.Spec.ExecutionMode())
		mode := "configured"
		if substrate.Spec.Discovery != nil && substrate.Spec.Discovery.Enabled {
			mode = "observe"
			if substrate.Spec.Discovery.ManagementPolicy != "" {
				mode = strings.ToLower(substrate.Spec.Discovery.ManagementPolicy)
			}
		}
		namespace := strings.TrimSpace(substrate.Spec.Parameters["namespace"])
		if namespace == "" {
			namespace = defaultSubstrateNamespace(kind)
		}
		details = append(details, fmt.Sprintf("%s substrate=%s execution=%s mode=%s namespace=%s", substrate.Name, kind, executionMode, mode, namespace))
		if kind == string(kaprov1alpha1.SubstrateKindArgo) || kind == string(kaprov1alpha1.SubstrateKindFlux) {
			var ns corev1.Namespace
			if err := c.Get(ctx, types.NamespacedName{Name: namespace}, &ns); err != nil {
				if apierrors.IsNotFound(err) {
					namespaceWarnings = append(namespaceWarnings, fmt.Sprintf("%s references missing namespace %s", substrate.Name, namespace))
				} else {
					namespaceWarnings = append(namespaceWarnings, fmt.Sprintf("%s namespace check failed: %v", substrate.Name, err))
				}
			}
		}
	}
	sort.Strings(details)
	sort.Strings(namespaceWarnings)
	if len(namespaceWarnings) > 0 {
		return doctorFinding{
			Name:    "gitops-substrates",
			Status:  doctorStatusWarn,
			Message: "some Argo/Flux substrate namespaces are not present",
			Details: append(limitDetails(namespaceWarnings, 8), limitDetails(details, 8)...),
		}
	}
	return doctorFinding{
		Name:    "gitops-substrates",
		Status:  doctorStatusPass,
		Message: fmt.Sprintf("%d Substrate object(s) configured", len(substrates.Items)),
		Details: limitDetails(details, 12),
	}
}

type doctorSecretRef struct {
	types.NamespacedName
	Source string
}

func collectReferencedPullSecrets(ctx context.Context, c client.Client, defaultNamespace string) ([]doctorSecretRef, []string, error) {
	seen := map[types.NamespacedName]doctorSecretRef{}
	var invalid []string
	addDefaultNamespaceRef := func(source, name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		key := types.NamespacedName{Namespace: defaultNamespace, Name: name}
		seen[key] = doctorSecretRef{NamespacedName: key, Source: source}
	}
	addTriggerRef := func(source string, ref *corev1.SecretReference) {
		if ref == nil || strings.TrimSpace(ref.Name) == "" {
			return
		}
		if strings.TrimSpace(ref.Namespace) == "" {
			invalid = append(invalid, source+": secretRef.namespace is required for cluster-scoped Trigger")
			return
		}
		key := types.NamespacedName{Namespace: strings.TrimSpace(ref.Namespace), Name: strings.TrimSpace(ref.Name)}
		seen[key] = doctorSecretRef{NamespacedName: key, Source: source}
	}
	var fleets kaprov1alpha1.FleetList
	if err := c.List(ctx, &fleets); err != nil && !apierrors.IsNotFound(err) {
		return nil, nil, err
	}
	for _, fleet := range fleets.Items {
		addDefaultNamespaceRef("Fleet/"+fleet.Name, fleet.Spec.Registry.SecretRef)
	}
	var substrates kaprov1alpha1.SubstrateList
	if err := c.List(ctx, &substrates); err != nil && !apierrors.IsNotFound(err) {
		return nil, nil, err
	}
	for _, substrate := range substrates.Items {
		for _, key := range []string{"secretRef", "pullSecret", "imagePullSecret"} {
			addDefaultNamespaceRef("Substrate/"+substrate.Name+" parameter "+key, substrate.Spec.Parameters[key])
		}
	}
	var triggers kaprov1alpha1.TriggerList
	if err := c.List(ctx, &triggers); err != nil && !apierrors.IsNotFound(err) {
		return nil, nil, err
	}
	for _, trigger := range triggers.Items {
		if trigger.Spec.Source.OCI != nil && trigger.Spec.Source.OCI.SecretRef != nil {
			addTriggerRef("Trigger/"+trigger.Name, trigger.Spec.Source.OCI.SecretRef)
		}
	}
	out := make([]doctorSecretRef, 0, len(seen))
	for _, ref := range seen {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	sort.Strings(invalid)
	return out, invalid, nil
}

func renderDoctorReport(report doctorReport) {
	cli.Header("kapro doctor")
	cli.KV("Overall", strings.ToUpper(report.Overall))
	tbl := cli.NewTable("CHECK", "STATUS", "MESSAGE")
	for _, f := range report.Checks {
		tbl.AddRow(f.Name, strings.ToUpper(f.Status), f.Message)
	}
	tbl.Render()
	for _, f := range report.Checks {
		if len(f.Details) == 0 {
			continue
		}
		cli.Muted(f.Name + " details:")
		for _, detail := range f.Details {
			cli.Muted("  - " + detail)
		}
	}
}

func countDoctorFailures(report doctorReport) int {
	failures := 0
	for _, f := range report.Checks {
		if f.Status == doctorStatusFail {
			failures++
		}
	}
	return failures
}

func prefixDetails(prefix string, items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, prefix+item)
	}
	return out
}

func limitDetails(items []string, n int) []string {
	if len(items) <= n {
		return items
	}
	out := append([]string{}, items[:n]...)
	out = append(out, fmt.Sprintf("... %d more", len(items)-n))
	return out
}

func accessKey(a authv1.ResourceAttributes) string {
	resource := a.Resource
	if a.Subresource != "" {
		resource += "/" + a.Subresource
	}
	return a.Verb + " " + a.Group + "/" + resource
}

var expectedKaproCRDs = []string{
	"substratediscoverypolicies.kapro.io",
	"argocdsubstrateconfigs.argocd.substrate.kapro.io",
	"approvals.kapro.io",
	"clusters.kapro.io",
	"clustertemplates.kapro.io",
	"decisiontraces.runtime.kapro.io",
	"deliveryunits.kapro.io",
	"fleets.kapro.io",
	"fluxsubstrateconfigs.flux.substrate.kapro.io",
	"kubernetesapplyconfigs.kubernetes.substrate.kapro.io",
	"ocibundleapplyconfigs.oci.substrate.kapro.io",
	"plans.kapro.io",
	"plugins.kapro.io",
	"policies.kapro.io",
	"promotionruns.runtime.kapro.io",
	"promotions.kapro.io",
	"sources.kapro.io",
	"substrateclasses.kapro.io",
	"substrates.kapro.io",
	"targets.runtime.kapro.io",
	"triggers.kapro.io",
}

func expectedCRDGroup(name string) string {
	_, group, _ := strings.Cut(name, ".")
	return group
}

var requiredOperatorAccess = []authv1.ResourceAttributes{
	{Group: "kapro.io", Resource: "substratediscoverypolicies", Verb: "get"},
	{Group: "kapro.io", Resource: "substratediscoverypolicies", Verb: "list"},
	{Group: "kapro.io", Resource: "substratediscoverypolicies", Verb: "watch"},
	{Group: "kapro.io", Resource: "substratediscoverypolicies", Subresource: "status", Verb: "update"},
	{Group: "argocd.substrate.kapro.io", Resource: "argocdsubstrateconfigs", Verb: "get"},
	{Group: "argocd.substrate.kapro.io", Resource: "argocdsubstrateconfigs", Verb: "list"},
	{Group: "argocd.substrate.kapro.io", Resource: "argocdsubstrateconfigs", Verb: "watch"},
	{Group: "kapro.io", Resource: "deliveryunits", Verb: "get"},
	{Group: "kapro.io", Resource: "deliveryunits", Verb: "list"},
	{Group: "kapro.io", Resource: "deliveryunits", Verb: "watch"},
	{Group: "kapro.io", Resource: "deliveryunits", Subresource: "status", Verb: "update"},
	{Group: "kapro.io", Resource: "fleets", Verb: "get"},
	{Group: "kapro.io", Resource: "fleets", Verb: "list"},
	{Group: "kapro.io", Resource: "fleets", Verb: "watch"},
	{Group: "kapro.io", Resource: "fleets", Verb: "patch"},
	{Group: "kapro.io", Resource: "fleets", Subresource: "status", Verb: "update"},
	{Group: "kapro.io", Resource: "clusters", Verb: "get"},
	{Group: "kapro.io", Resource: "clusters", Verb: "list"},
	{Group: "kapro.io", Resource: "clusters", Verb: "watch"},
	{Group: "kapro.io", Resource: "clusters", Verb: "patch"},
	{Group: "kapro.io", Resource: "clusters", Subresource: "status", Verb: "update"},
	{Group: "kapro.io", Resource: "plans", Verb: "get"},
	{Group: "kapro.io", Resource: "plans", Verb: "list"},
	{Group: "kapro.io", Resource: "plans", Verb: "watch"},
	{Group: "kapro.io", Resource: "substrateclasses", Verb: "get"},
	{Group: "kapro.io", Resource: "substrateclasses", Verb: "list"},
	{Group: "kapro.io", Resource: "substrateclasses", Verb: "watch"},
	{Group: "kapro.io", Resource: "substrateclasses", Subresource: "status", Verb: "update"},
	{Group: "kapro.io", Resource: "substrates", Verb: "get"},
	{Group: "kapro.io", Resource: "substrates", Verb: "list"},
	{Group: "kapro.io", Resource: "substrates", Verb: "watch"},
	{Group: "kapro.io", Resource: "substrates", Subresource: "status", Verb: "update"},
	{Group: "flux.substrate.kapro.io", Resource: "fluxsubstrateconfigs", Verb: "get"},
	{Group: "flux.substrate.kapro.io", Resource: "fluxsubstrateconfigs", Verb: "list"},
	{Group: "flux.substrate.kapro.io", Resource: "fluxsubstrateconfigs", Verb: "watch"},
	{Group: "kubernetes.substrate.kapro.io", Resource: "kubernetesapplyconfigs", Verb: "get"},
	{Group: "kubernetes.substrate.kapro.io", Resource: "kubernetesapplyconfigs", Verb: "list"},
	{Group: "kubernetes.substrate.kapro.io", Resource: "kubernetesapplyconfigs", Verb: "watch"},
	{Group: "oci.substrate.kapro.io", Resource: "ocibundleapplyconfigs", Verb: "get"},
	{Group: "oci.substrate.kapro.io", Resource: "ocibundleapplyconfigs", Verb: "list"},
	{Group: "oci.substrate.kapro.io", Resource: "ocibundleapplyconfigs", Verb: "watch"},
	{Group: "apps", Resource: "deployments", Verb: "get"},
	{Group: "apps", Resource: "deployments", Verb: "list"},
	{Group: "apps", Resource: "deployments", Verb: "watch"},
	{Group: "apps", Resource: "deployments", Verb: "patch"},
	{Group: "apps", Resource: "deployments", Verb: "update"},
	{Group: "kapro.io", Resource: "promotions", Verb: "get"},
	{Group: "kapro.io", Resource: "promotions", Verb: "list"},
	{Group: "kapro.io", Resource: "promotions", Verb: "watch"},
	{Group: "kapro.io", Resource: "promotions", Subresource: "status", Verb: "update"},
	{Group: "runtime.kapro.io", Resource: "promotionruns", Verb: "create"},
	{Group: "runtime.kapro.io", Resource: "promotionruns", Verb: "get"},
	{Group: "runtime.kapro.io", Resource: "promotionruns", Verb: "list"},
	{Group: "runtime.kapro.io", Resource: "promotionruns", Verb: "watch"},
	{Group: "runtime.kapro.io", Resource: "promotionruns", Subresource: "status", Verb: "update"},
	{Group: "runtime.kapro.io", Resource: "decisiontraces", Verb: "create"},
	{Group: "runtime.kapro.io", Resource: "decisiontraces", Verb: "get"},
	{Group: "runtime.kapro.io", Resource: "decisiontraces", Subresource: "status", Verb: "update"},
	{Group: "runtime.kapro.io", Resource: "targets", Verb: "create"},
	{Group: "runtime.kapro.io", Resource: "targets", Verb: "get"},
	{Group: "runtime.kapro.io", Resource: "targets", Verb: "list"},
	{Group: "runtime.kapro.io", Resource: "targets", Verb: "watch"},
	{Group: "runtime.kapro.io", Resource: "targets", Subresource: "status", Verb: "update"},
}

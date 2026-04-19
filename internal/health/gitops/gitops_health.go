// Package gitops implements health.Assessor using algorithms ported from
// argoproj/gitops-engine/pkg/health (Apache-2.0).
//
// gitops-engine v0.7.3 pins k8s.io/kubernetes v1.23.1, which is incompatible
// with this module's k8s.io/api v0.35.x. We therefore inline the assessment
// functions directly, using the typed k8s.io/api/apps/v1 and batch/v1 structs
// already present in this module. The algorithms are identical.
package gitops

import (
"context"
"fmt"

appsv1 "k8s.io/api/apps/v1"
batchv1 "k8s.io/api/batch/v1"
ctrl "sigs.k8s.io/controller-runtime"
"sigs.k8s.io/controller-runtime/pkg/client"

pkghealth "kapro.io/kapro/pkg/health"
)

// defaultKinds are the resource kinds assessed when AssessRequest.Kinds is empty.
var defaultKinds = []string{"Deployment", "StatefulSet", "DaemonSet"}

// Assessor implements pkghealth.Assessor using inline health-check logic
// ported from argoproj/gitops-engine/pkg/health.
type Assessor struct {
Client client.Client // controller-runtime client (in-cluster)
}

var _ pkghealth.Assessor = &Assessor{}

// AssessHealth lists workloads in the requested namespace and evaluates their
// health using per-kind check functions mirroring gitops-engine behaviour.
//
// Overall status is the worst across all resources using the order:
// Degraded > Unknown > Missing > Progressing > Suspended > Healthy.
func (a *Assessor) AssessHealth(ctx context.Context, req pkghealth.AssessRequest) (pkghealth.AssessResult, error) {
log := ctrl.LoggerFrom(ctx).WithName("health-assessor")

kinds := req.Kinds
if len(kinds) == 0 {
kinds = defaultKinds
}

var resources []pkghealth.ResourceHealth
overall := pkghealth.StatusHealthy

for _, kind := range kinds {
var kindResources []pkghealth.ResourceHealth
var err error

switch kind {
case "Deployment":
kindResources, err = a.assessDeployments(ctx, req.Namespace)
case "StatefulSet":
kindResources, err = a.assessStatefulSets(ctx, req.Namespace)
case "DaemonSet":
kindResources, err = a.assessDaemonSets(ctx, req.Namespace)
case "Job":
kindResources, err = a.assessJobs(ctx, req.Namespace)
default:
log.Info("unknown kind, skipping", "kind", kind)
continue
}

if err != nil {
return pkghealth.AssessResult{}, fmt.Errorf("health: assess %s: %w", kind, err)
}

resources = append(resources, kindResources...)
for _, r := range kindResources {
if isWorse(overall, r.Status) {
overall = r.Status
}
}
}

result := pkghealth.AssessResult{
Overall:   overall,
Resources: resources,
}
if result.Overall != pkghealth.StatusHealthy {
result.Message = summaryMessage(resources)
}

log.Info("health assessed", "overall", result.Overall, "resources", len(resources))
return result, nil
}

// ── per-kind assessors ────────────────────────────────────────────────────────

func (a *Assessor) assessDeployments(ctx context.Context, ns string) ([]pkghealth.ResourceHealth, error) {
var list appsv1.DeploymentList
if err := a.Client.List(ctx, &list, listOpts(ns)...); err != nil {
return nil, fmt.Errorf("list Deployments: %w", err)
}

out := make([]pkghealth.ResourceHealth, 0, len(list.Items))
for i := range list.Items {
d := &list.Items[i]
hs := deploymentHealth(d)
out = append(out, pkghealth.ResourceHealth{
Group:     "apps",
Kind:      "Deployment",
Namespace: d.Namespace,
Name:      d.Name,
Status:    hs.status,
Message:   hs.message,
})
}
return out, nil
}

func (a *Assessor) assessStatefulSets(ctx context.Context, ns string) ([]pkghealth.ResourceHealth, error) {
var list appsv1.StatefulSetList
if err := a.Client.List(ctx, &list, listOpts(ns)...); err != nil {
return nil, fmt.Errorf("list StatefulSets: %w", err)
}

out := make([]pkghealth.ResourceHealth, 0, len(list.Items))
for i := range list.Items {
s := &list.Items[i]
hs := statefulSetHealth(s)
out = append(out, pkghealth.ResourceHealth{
Group:     "apps",
Kind:      "StatefulSet",
Namespace: s.Namespace,
Name:      s.Name,
Status:    hs.status,
Message:   hs.message,
})
}
return out, nil
}

func (a *Assessor) assessDaemonSets(ctx context.Context, ns string) ([]pkghealth.ResourceHealth, error) {
var list appsv1.DaemonSetList
if err := a.Client.List(ctx, &list, listOpts(ns)...); err != nil {
return nil, fmt.Errorf("list DaemonSets: %w", err)
}

out := make([]pkghealth.ResourceHealth, 0, len(list.Items))
for i := range list.Items {
d := &list.Items[i]
hs := daemonSetHealth(d)
out = append(out, pkghealth.ResourceHealth{
Group:     "apps",
Kind:      "DaemonSet",
Namespace: d.Namespace,
Name:      d.Name,
Status:    hs.status,
Message:   hs.message,
})
}
return out, nil
}

func (a *Assessor) assessJobs(ctx context.Context, ns string) ([]pkghealth.ResourceHealth, error) {
var list batchv1.JobList
if err := a.Client.List(ctx, &list, listOpts(ns)...); err != nil {
return nil, fmt.Errorf("list Jobs: %w", err)
}

out := make([]pkghealth.ResourceHealth, 0, len(list.Items))
for i := range list.Items {
j := &list.Items[i]
hs := jobHealth(j)
out = append(out, pkghealth.ResourceHealth{
Group:     "batch",
Kind:      "Job",
Namespace: j.Namespace,
Name:      j.Name,
Status:    hs.status,
Message:   hs.message,
})
}
return out, nil
}

// ── health-check algorithms (ported from gitops-engine, Apache-2.0) ───────────

type healthResult struct {
status  pkghealth.Status
message string
}

// deploymentHealth mirrors getAppsv1DeploymentHealth from gitops-engine.
// Ref: https://github.com/argoproj/gitops-engine/blob/v0.7.3/pkg/health/health_deployment.go
func deploymentHealth(d *appsv1.Deployment) healthResult {
if d.DeletionTimestamp != nil {
return healthResult{pkghealth.StatusProgressing, "Pending deletion"}
}
if d.Spec.Paused {
return healthResult{pkghealth.StatusSuspended, "Deployment is paused"}
}
if d.Generation <= d.Status.ObservedGeneration {
for i := range d.Status.Conditions {
c := d.Status.Conditions[i]
if c.Type == appsv1.DeploymentProgressing && c.Reason == "ProgressDeadlineExceeded" {
return healthResult{
pkghealth.StatusDegraded,
fmt.Sprintf("Deployment %q exceeded its progress deadline", d.Name),
}
}
}
desired := int32(1)
if d.Spec.Replicas != nil {
desired = *d.Spec.Replicas
}
switch {
case d.Status.UpdatedReplicas < desired:
return healthResult{
pkghealth.StatusProgressing,
fmt.Sprintf("Waiting for rollout to finish: %d out of %d new replicas have been updated...", d.Status.UpdatedReplicas, desired),
}
case d.Status.Replicas > d.Status.UpdatedReplicas:
return healthResult{
pkghealth.StatusProgressing,
fmt.Sprintf("Waiting for rollout to finish: %d old replicas are pending termination...", d.Status.Replicas-d.Status.UpdatedReplicas),
}
case d.Status.AvailableReplicas < d.Status.UpdatedReplicas:
return healthResult{
pkghealth.StatusProgressing,
fmt.Sprintf("Waiting for rollout to finish: %d of %d updated replicas are available...", d.Status.AvailableReplicas, d.Status.UpdatedReplicas),
}
}
} else {
return healthResult{
pkghealth.StatusProgressing,
"Waiting for rollout to finish: observed deployment generation less than desired generation",
}
}
return healthResult{status: pkghealth.StatusHealthy}
}

// statefulSetHealth mirrors getAppsv1StatefulSetHealth from gitops-engine.
// Ref: https://github.com/argoproj/gitops-engine/blob/v0.7.3/pkg/health/health_statefulset.go
func statefulSetHealth(s *appsv1.StatefulSet) healthResult {
if s.DeletionTimestamp != nil {
return healthResult{pkghealth.StatusProgressing, "Pending deletion"}
}
if s.Status.ObservedGeneration == 0 || s.Generation > s.Status.ObservedGeneration {
return healthResult{pkghealth.StatusProgressing, "Waiting for statefulset spec update to be observed..."}
}
desired := int32(1)
if s.Spec.Replicas != nil {
desired = *s.Spec.Replicas
}
if s.Status.ReadyReplicas < desired {
return healthResult{
pkghealth.StatusProgressing,
fmt.Sprintf("Waiting for %d pods to be ready...", desired-s.Status.ReadyReplicas),
}
}
if s.Spec.UpdateStrategy.Type == appsv1.RollingUpdateStatefulSetStrategyType &&
s.Spec.UpdateStrategy.RollingUpdate != nil &&
s.Spec.Replicas != nil && s.Spec.UpdateStrategy.RollingUpdate.Partition != nil {
want := *s.Spec.Replicas - *s.Spec.UpdateStrategy.RollingUpdate.Partition
if s.Status.UpdatedReplicas < want {
return healthResult{
pkghealth.StatusProgressing,
fmt.Sprintf("Waiting for partitioned roll out to finish: %d out of %d new pods have been updated...", s.Status.UpdatedReplicas, want),
}
}
return healthResult{
pkghealth.StatusHealthy,
fmt.Sprintf("partitioned roll out complete: %d new pods have been updated...", s.Status.UpdatedReplicas),
}
}
if s.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType {
return healthResult{
pkghealth.StatusHealthy,
fmt.Sprintf("statefulset has %d ready pods", s.Status.ReadyReplicas),
}
}
if s.Status.UpdateRevision != s.Status.CurrentRevision {
return healthResult{
pkghealth.StatusProgressing,
fmt.Sprintf("waiting for statefulset rolling update to complete %d pods at revision %s...", s.Status.UpdatedReplicas, s.Status.UpdateRevision),
}
}
return healthResult{
pkghealth.StatusHealthy,
fmt.Sprintf("statefulset rolling update complete %d pods at revision %s...", s.Status.CurrentReplicas, s.Status.CurrentRevision),
}
}

// daemonSetHealth mirrors getAppsv1DaemonSetHealth from gitops-engine.
// Ref: https://github.com/argoproj/gitops-engine/blob/v0.7.3/pkg/health/health_daemonset.go
func daemonSetHealth(d *appsv1.DaemonSet) healthResult {
if d.DeletionTimestamp != nil {
return healthResult{pkghealth.StatusProgressing, "Pending deletion"}
}
if d.Generation <= d.Status.ObservedGeneration {
if d.Spec.UpdateStrategy.Type == appsv1.OnDeleteDaemonSetStrategyType {
return healthResult{
pkghealth.StatusHealthy,
fmt.Sprintf("daemon set %d out of %d new pods have been updated", d.Status.UpdatedNumberScheduled, d.Status.DesiredNumberScheduled),
}
}
switch {
case d.Status.UpdatedNumberScheduled < d.Status.DesiredNumberScheduled:
return healthResult{
pkghealth.StatusProgressing,
fmt.Sprintf("Waiting for daemon set %q rollout to finish: %d out of %d new pods have been updated...", d.Name, d.Status.UpdatedNumberScheduled, d.Status.DesiredNumberScheduled),
}
case d.Status.NumberAvailable < d.Status.DesiredNumberScheduled:
return healthResult{
pkghealth.StatusProgressing,
fmt.Sprintf("Waiting for daemon set %q rollout to finish: %d of %d updated pods are available...", d.Name, d.Status.NumberAvailable, d.Status.DesiredNumberScheduled),
}
}
} else {
return healthResult{
pkghealth.StatusProgressing,
"Waiting for rollout to finish: observed daemon set generation less than desired generation",
}
}
return healthResult{status: pkghealth.StatusHealthy}
}

// jobHealth mirrors getJobHealth from gitops-engine.
// Ref: https://github.com/argoproj/gitops-engine/blob/v0.7.3/pkg/health/health_job.go
func jobHealth(j *batchv1.Job) healthResult {
if j.DeletionTimestamp != nil {
return healthResult{pkghealth.StatusProgressing, "Pending deletion"}
}
for _, c := range j.Status.Conditions {
switch c.Type {
case batchv1.JobFailed:
return healthResult{pkghealth.StatusDegraded, c.Message}
case batchv1.JobComplete:
return healthResult{pkghealth.StatusHealthy, c.Message}
}
}
return healthResult{pkghealth.StatusProgressing, "Waiting for job to complete..."}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func listOpts(ns string) []client.ListOption {
if ns == "" {
return nil
}
return []client.ListOption{client.InNamespace(ns)}
}

// healthOrder mirrors gitops-engine's ordering (most → least healthy).
var healthOrder = []pkghealth.Status{
pkghealth.StatusHealthy,
pkghealth.StatusSuspended,
pkghealth.StatusProgressing,
pkghealth.StatusMissing,
pkghealth.StatusDegraded,
pkghealth.StatusUnknown,
}

// isWorse returns true when newS is a worse health status than current.
func isWorse(current, newS pkghealth.Status) bool {
ci, ni := 0, 0
for i, s := range healthOrder {
if s == current {
ci = i
}
if s == newS {
ni = i
}
}
return ni > ci
}

// summaryMessage returns the first non-healthy resource's description,
// preferring Degraded over others.
func summaryMessage(resources []pkghealth.ResourceHealth) string {
for _, r := range resources {
if r.Status == pkghealth.StatusDegraded {
return fmt.Sprintf("%s/%s is %s: %s", r.Kind, r.Name, r.Status, r.Message)
}
}
for _, r := range resources {
if r.Status != pkghealth.StatusHealthy {
return fmt.Sprintf("%s/%s is %s: %s", r.Kind, r.Name, r.Status, r.Message)
}
}
return ""
}

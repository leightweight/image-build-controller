/*
Copyright 2026 The image-build-controller Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	aclapi "github.com/fluxcd/pkg/apis/acl"
	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/conditions"
	"github.com/fluxcd/pkg/runtime/patch"
	"github.com/fluxcd/pkg/runtime/predicates"
	"github.com/fluxcd/pkg/runtime/reconcile"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	buildv1alpha1 "github.com/leightweight/image-build-controller/api/v1alpha1"
	"github.com/leightweight/image-build-controller/internal/build"
)

const (
	// ControllerName is the field owner for status patches and the name on recorded Events.
	ControllerName = "image-build-controller"

	// sourceRefIndexKey indexes ImageBuilds by the namespace/name of the GitRepository they reference.
	sourceRefIndexKey = ".spec.sourceRef"

	// recreateDelay is how long to wait for an old Job with the same name to go away before creating its replacement.
	recreateDelay = 2 * time.Second
)

// ownedConditions are the condition types this reconciler manages.
var ownedConditions = []string{
	meta.ReadyCondition,
	meta.ReconcilingCondition,
	meta.StalledCondition,
}

// ImageBuildReconciler reconciles ImageBuild objects.
type ImageBuildReconciler struct {
	client.Client
	EventRecorder events.EventRecorder

	// NoCrossNamespaceRefs rejects a sourceRef in a namespace other than the ImageBuild's.
	NoCrossNamespaceRefs bool

	// BuildConfig holds the controller-wide settings for build Jobs.
	BuildConfig build.Config

	// Now returns the current time. Tests replace it; nil means time.Now.
	Now func() time.Time
}

// +kubebuilder:rbac:groups=build.fluxcd.leightweight.io,resources=imagebuilds,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=build.fluxcd.leightweight.io,resources=imagebuilds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=build.fluxcd.leightweight.io,resources=imagebuilds/finalizers,verbs=update
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile resolves the ImageBuild's source, runs a build Job when the build key changes,
// and records the result in its status.
func (r *ImageBuildReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	log := logf.FromContext(ctx)

	obj := &buildv1alpha1.ImageBuild{}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	serialPatcher := patch.NewSerialPatcher(obj, r.Client)
	oldReady := conditions.Get(obj, meta.ReadyCondition).DeepCopy()

	// Always patch the status at the end, so observedGeneration, conditions,
	// and lastHandledReconcileAt reflect this reconciliation.
	defer func() {
		if err := r.patchStatus(ctx, obj, serialPatcher); err != nil {
			if !obj.GetDeletionTimestamp().IsZero() {
				err = kerrors.FilterOut(err, apierrors.IsNotFound)
			}
			retErr = kerrors.NewAggregate([]error{retErr, err})
		}
		r.recordReadyChange(obj, oldReady)
	}()

	if obj.Spec.Suspend {
		log.Info("Skipped reconciliation because the ImageBuild is suspended")
		return ctrl.Result{}, nil
	}

	return r.reconcile(ctx, obj, serialPatcher)
}

func (r *ImageBuildReconciler) patchStatus(ctx context.Context, obj *buildv1alpha1.ImageBuild,
	serialPatcher *patch.SerialPatcher) error {
	// A reconcile request counts as handled even when the ImageBuild is suspended.
	if token, ok := meta.ReconcileAnnotationValue(obj.GetAnnotations()); ok {
		obj.Status.SetLastHandledReconcileRequest(token)
	}
	return serialPatcher.Patch(ctx, obj, reconcile.AddPatchOptions(obj, nil, ownedConditions, ControllerName)...)
}

func (r *ImageBuildReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// reconcile does the work for a non-suspended ImageBuild and finalizes its conditions to follow kstatus.
func (r *ImageBuildReconciler) reconcile(ctx context.Context, obj *buildv1alpha1.ImageBuild,
	serialPatcher *patch.SerialPatcher) (result ctrl.Result, retErr error) {
	// upToDate is set when the last build matches the current build key, so there's nothing left to do.
	upToDate := false
	// building is set while a build Job runs; watches on the Job and its pods bring the next reconciliation.
	building := false

	defer func() {
		isSuccess := func(_ ctrl.Result, err error) bool {
			return err == nil && upToDate
		}
		rs := reconcile.NewResultFinalizer(isSuccess, "ImageBuild is ready")
		retErr = rs.Finalize(obj, result, retErr)

		// A Reconciling condition left after finalizing means this reconciliation will be retried,
		// unless a build is running.
		if conditions.IsReconciling(obj) && !building {
			reconciling := conditions.Get(obj, meta.ReconcilingCondition)
			reconciling.Reason = meta.ProgressingWithRetryReason
			conditions.Set(obj, reconciling)
		}
	}()

	reconcile.ProgressiveStatus(false, obj, meta.ProgressingReason, "Reconciliation in progress")
	interval := obj.Spec.Interval.Duration

	var src *build.Source
	if obj.Spec.SourceRef != nil {
		repo, err := r.reconcileSource(ctx, obj)
		if err != nil {
			return ctrl.Result{}, err
		}
		if repo == nil {
			// A stalled ImageBuild waits for a spec change instead of retrying.
			if conditions.IsStalled(obj) {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{RequeueAfter: interval}, nil
		}
		src = sourceFromGitRepository(repo)
	}

	now := r.now()
	key := build.Key(obj, src, now)

	jobs, err := r.ownedJobs(ctx, obj)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.recordFinishedJobs(ctx, obj, jobs, serialPatcher); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.pruneHistory(ctx, obj, jobs); err != nil {
		return ctrl.Result{}, err
	}

	// A running Job for the current key is the build in progress, including one created before a restart.
	var current, older *batchv1.Job
	for i := range jobs {
		job := &jobs[i]
		if finished, _, _ := jobFinished(job); finished || jobDeleting(job) {
			continue
		}
		if jobKey(job) == key {
			current = job
		} else if older == nil {
			older = job
		}
	}

	if older != nil {
		if obj.Spec.ConcurrencyPolicy == buildv1alpha1.WaitConcurrency {
			building = true
			markBuilding(obj, older, buildv1alpha1.WaitingForBuildReason,
				"Waiting for build Job %s to finish before building the latest changes", older.Name)
			return ctrl.Result{RequeueAfter: interval}, nil
		}
		logf.FromContext(ctx).Info("Replaced an outdated build", "job", klog.KObj(older))
		if err := deleteJob(ctx, r.Client, older); err != nil {
			return ctrl.Result{}, err
		}
	}

	if current != nil {
		building = true
		markBuilding(obj, current, buildv1alpha1.BuildingReason, "Building %s with Job %s",
			obj.Spec.Push.Image, current.Name)
		return ctrl.Result{RequeueAfter: interval}, nil
	}
	obj.Status.CurrentBuild = nil

	if last := obj.Status.LastBuild; last != nil && last.BuildKey == key {
		upToDate = true
		markLastBuild(obj)
		return ctrl.Result{RequeueAfter: requeueAfter(obj, now)}, nil
	}

	return r.startBuild(ctx, obj, src, key, now, jobs, &building)
}

// startBuild creates the build Job for the current build key.
func (r *ImageBuildReconciler) startBuild(ctx context.Context, obj *buildv1alpha1.ImageBuild, src *build.Source,
	key string, now time.Time, jobs []batchv1.Job, building *bool) (ctrl.Result, error) {
	job, err := build.Job(obj, src, r.BuildConfig, key, now)
	if err != nil {
		conditions.MarkStalled(obj, buildv1alpha1.InvalidTagsReason, "%s", err.Error())
		conditions.MarkFalse(obj, meta.ReadyCondition, buildv1alpha1.InvalidTagsReason, "%s", err.Error())
		return ctrl.Result{}, nil
	}

	// A finished Job with the same name is left over from an earlier build of the same key.
	// Replace it, so the new build gets fresh tags.
	for i := range jobs {
		if jobs[i].Name != job.Name {
			continue
		}
		if !jobDeleting(&jobs[i]) {
			if err := deleteJob(ctx, r.Client, &jobs[i]); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: recreateDelay}, nil
	}

	if err := r.Create(ctx, job); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ctrl.Result{RequeueAfter: recreateDelay}, nil
		}
		return ctrl.Result{}, fmt.Errorf("could not create build Job %s: %w", job.Name, err)
	}
	logf.FromContext(ctx).Info("Started a build", "job", klog.KObj(job), "buildKey", key)

	*building = true
	markBuilding(obj, job, buildv1alpha1.BuildingReason, "Building %s with Job %s", obj.Spec.Push.Image, job.Name)
	return ctrl.Result{RequeueAfter: obj.Spec.Interval.Duration}, nil
}

// recordFinishedJobs records the result of each finished Job that the status doesn't describe yet,
// persists the status, and then releases the Jobs' pods.
func (r *ImageBuildReconciler) recordFinishedJobs(ctx context.Context, obj *buildv1alpha1.ImageBuild,
	jobs []batchv1.Job, serialPatcher *patch.SerialPatcher) error {
	var toRelease []*batchv1.Job
	changed := false
	for i := range jobs {
		job := &jobs[i]
		finished, _, _ := jobFinished(job)
		if !finished || jobDeleting(job) {
			continue
		}
		toRelease = append(toRelease, job)
		if !pending(obj, job) {
			continue
		}
		out, err := r.outcome(ctx, job)
		if err != nil {
			return err
		}
		r.record(obj, job, out)
		changed = true
	}

	// Persist the results before releasing the pods, so a failed patch can't lose a termination message.
	if changed {
		if err := r.patchStatus(ctx, obj, serialPatcher); err != nil {
			return fmt.Errorf("could not record build results: %w", err)
		}
	}
	for _, job := range toRelease {
		if err := releasePods(ctx, r.Client, job); err != nil {
			return err
		}
	}
	return nil
}

// record writes a finished Job's result into the ImageBuild status and records an Event.
func (r *ImageBuildReconciler) record(obj *buildv1alpha1.ImageBuild, job *batchv1.Job, out jobOutcome) {
	startedAt := job.CreationTimestamp
	finishedAt := out.finishedAt
	last := &buildv1alpha1.LastBuild{
		BuildKey:       jobKey(job),
		SourceRevision: job.Annotations[build.SourceRevisionAnnotation],
		StartedAt:      &startedAt,
		FinishedAt:     &finishedAt,
		JobName:        job.Name,
		Result:         buildv1alpha1.BuildFailed,
	}
	if cb := obj.Status.CurrentBuild; cb != nil && cb.JobName == job.Name {
		obj.Status.CurrentBuild = nil
	}

	if !out.succeeded {
		obj.Status.LastBuild = last
		message := fmt.Sprintf("Build Job %s failed", job.Name)
		if out.message != "" {
			message += ": " + out.message
		}
		conditions.MarkStalled(obj, buildv1alpha1.BuildFailedReason, "%s", message)
		conditions.MarkFalse(obj, meta.ReadyCondition, buildv1alpha1.BuildFailedReason, "%s", message)
		r.event(obj, job, corev1.EventTypeWarning, buildv1alpha1.BuildFailedReason, message)
		return
	}

	image := job.Annotations[build.ImageAnnotation]
	var tags []string
	if t := job.Annotations[build.TagsAnnotation]; t != "" {
		tags = strings.Split(t, ",")
	}
	last.Result = buildv1alpha1.BuildSucceeded
	last.Image = &buildv1alpha1.BuiltImage{Tags: tags, Digest: out.digest}
	obj.Status.LastBuild = last
	switch {
	case out.digest != "":
		obj.Status.LatestImage = image + "@" + out.digest
	case len(tags) > 0:
		obj.Status.LatestImage = image + ":" + tags[0]
	}
	message := "Pushed " + obj.Status.LatestImage
	conditions.Delete(obj, meta.StalledCondition)
	conditions.MarkTrue(obj, meta.ReadyCondition, meta.SucceededReason, "%s", message)
	r.event(obj, job, corev1.EventTypeNormal, meta.SucceededReason, message)
}

// pruneHistory deletes finished Jobs beyond the history limits, oldest first.
// It keeps Jobs whose results aren't recorded yet.
func (r *ImageBuildReconciler) pruneHistory(ctx context.Context, obj *buildv1alpha1.ImageBuild, jobs []batchv1.Job) error {
	var succeeded, failed []*batchv1.Job
	for i := range jobs {
		job := &jobs[i]
		finished, ok, _ := jobFinished(job)
		if !finished || jobDeleting(job) || pending(obj, job) {
			continue
		}
		if ok {
			succeeded = append(succeeded, job)
		} else {
			failed = append(failed, job)
		}
	}
	for _, group := range []struct {
		jobs  []*batchv1.Job
		limit *int32
	}{
		{succeeded, obj.Spec.SuccessfulBuildsHistoryLimit},
		{failed, obj.Spec.FailedBuildsHistoryLimit},
	} {
		limit := 0
		if group.limit != nil {
			limit = int(*group.limit)
		}
		// jobs is oldest first, so the excess is at the start.
		for i := 0; i < len(group.jobs)-limit; i++ {
			if err := deleteJob(ctx, r.Client, group.jobs[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

// markBuilding reports a running build Job.
func markBuilding(obj *buildv1alpha1.ImageBuild, job *batchv1.Job, reason, messageFormat string, args ...any) {
	startedAt := job.CreationTimestamp
	obj.Status.CurrentBuild = &buildv1alpha1.CurrentBuild{
		JobName:        job.Name,
		SourceRevision: job.Annotations[build.SourceRevisionAnnotation],
		StartedAt:      &startedAt,
	}
	conditions.Delete(obj, meta.StalledCondition)
	reconcile.ProgressiveStatus(true, obj, reason, messageFormat, args...)
}

// markLastBuild reports the result of the last build when nothing has changed since.
func markLastBuild(obj *buildv1alpha1.ImageBuild) {
	if obj.Status.LastBuild.Result == buildv1alpha1.BuildSucceeded {
		conditions.Delete(obj, meta.StalledCondition)
		conditions.MarkTrue(obj, meta.ReadyCondition, meta.SucceededReason, "Pushed %s", obj.Status.LatestImage)
		return
	}
	// Keep the failure message recorded when the build finished, because the pod may be gone by now.
	message := fmt.Sprintf("Build Job %s failed", obj.Status.LastBuild.JobName)
	if ready := conditions.Get(obj, meta.ReadyCondition); ready != nil && ready.Reason == buildv1alpha1.BuildFailedReason {
		message = ready.Message
	}
	conditions.MarkStalled(obj, buildv1alpha1.BuildFailedReason, "%s", message)
	conditions.MarkFalse(obj, meta.ReadyCondition, buildv1alpha1.BuildFailedReason, "%s", message)
}

// requeueAfter returns when to check the ImageBuild again: after spec.interval,
// or just after the current rebuildInterval period ends, whichever comes first.
func requeueAfter(obj *buildv1alpha1.ImageBuild, now time.Time) time.Duration {
	next := obj.Spec.Interval.Duration
	if ri := obj.Spec.RebuildInterval; ri != nil && ri.Duration > 0 {
		period := now.UnixNano() / ri.Nanoseconds()
		end := time.Unix(0, (period+1)*ri.Nanoseconds())
		// A second past the boundary, the build key has changed.
		if d := end.Sub(now) + time.Second; next <= 0 || d < next {
			next = d
		}
	}
	return next
}

func sourceFromGitRepository(repo *sourcev1.GitRepository) *build.Source {
	artifact := repo.GetArtifact()
	ref := repo.Spec.Reference
	return &build.Source{
		URL:      artifact.URL,
		Digest:   artifact.Digest,
		Revision: artifact.Revision,
		Tag:      ref != nil && (ref.Tag != "" || ref.SemVer != ""),
	}
}

func (r *ImageBuildReconciler) event(obj *buildv1alpha1.ImageBuild, job *batchv1.Job, eventType, reason, message string) {
	if r.EventRecorder == nil {
		return
	}
	r.EventRecorder.Eventf(obj, job, eventType, reason, "Build", "%s", truncateEventNote(message))
}

// maxEventNoteLength is the longest note the API server accepts on an events.k8s.io Event.
const maxEventNoteLength = 1024

// truncateEventNote shortens a message to fit in an Event, keeping its end, where a build log shows the error.
func truncateEventNote(message string) string {
	if len(message) <= maxEventNoteLength {
		return message
	}
	const ellipsis = "..."
	start := len(message) - (maxEventNoteLength - len(ellipsis))
	// Don't start in the middle of a multi-byte character.
	for start < len(message) && !utf8.RuneStart(message[start]) {
		start++
	}
	return ellipsis + message[start:]
}

// reconcileSource returns the referenced GitRepository when it has an artifact to build from.
// It returns nil with conditions set when the build has to wait for the source.
func (r *ImageBuildReconciler) reconcileSource(ctx context.Context, obj *buildv1alpha1.ImageBuild) (*sourcev1.GitRepository, error) {
	ref := sourceRefKey(obj)

	if r.NoCrossNamespaceRefs && ref.Namespace != obj.Namespace {
		conditions.MarkStalled(obj, aclapi.AccessDeniedReason,
			"Cross-namespace references are not allowed: %s %s", obj.Spec.SourceRef.Kind, ref)
		conditions.MarkFalse(obj, meta.ReadyCondition, aclapi.AccessDeniedReason,
			"Cross-namespace references are not allowed: %s %s", obj.Spec.SourceRef.Kind, ref)
		return nil, nil
	}

	source := &sourcev1.GitRepository{}
	if err := r.Get(ctx, ref, source); err != nil {
		if apierrors.IsNotFound(err) {
			markSourceNotReady(obj, "GitRepository %s was not found", ref)
			return nil, nil
		}
		return nil, fmt.Errorf("could not get GitRepository %s: %w", ref, err)
	}

	// Like the Flux controllers, build from the last artifact even if the source's latest fetch failed.
	if source.GetArtifact() == nil {
		markSourceNotReady(obj, "GitRepository %s has no artifact yet", ref)
		return nil, nil
	}
	return source, nil
}

func markSourceNotReady(obj *buildv1alpha1.ImageBuild, messageFormat string, args ...any) {
	conditions.MarkReconciling(obj, buildv1alpha1.SourceNotReadyReason, messageFormat, args...)
	conditions.MarkFalse(obj, meta.ReadyCondition, buildv1alpha1.SourceNotReadyReason, messageFormat, args...)
}

// recordReadyChange records an Event when the Ready condition's status or reason changes.
// Finished builds record their own Event, so it skips their reasons.
func (r *ImageBuildReconciler) recordReadyChange(obj *buildv1alpha1.ImageBuild, oldReady *metav1.Condition) {
	if r.EventRecorder == nil {
		return
	}
	newReady := conditions.Get(obj, meta.ReadyCondition)
	if newReady == nil || newReady.Reason == meta.SucceededReason || newReady.Reason == buildv1alpha1.BuildFailedReason {
		return
	}
	if oldReady != nil && oldReady.Status == newReady.Status && oldReady.Reason == newReady.Reason {
		return
	}
	eventType := corev1.EventTypeNormal
	if newReady.Status == metav1.ConditionFalse {
		eventType = corev1.EventTypeWarning
	}
	r.EventRecorder.Eventf(obj, nil, eventType, newReady.Reason, "Reconcile", "%s", newReady.Message)
}

// sourceRefKey returns the namespaced name of the ImageBuild's source, defaulting to the ImageBuild's namespace.
func sourceRefKey(obj *buildv1alpha1.ImageBuild) types.NamespacedName {
	namespace := obj.Spec.SourceRef.Namespace
	if namespace == "" {
		namespace = obj.Namespace
	}
	return types.NamespacedName{Namespace: namespace, Name: obj.Spec.SourceRef.Name}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ImageBuildReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(ctx, &buildv1alpha1.ImageBuild{}, sourceRefIndexKey,
		func(o client.Object) []string {
			obj := o.(*buildv1alpha1.ImageBuild)
			if obj.Spec.SourceRef == nil {
				return nil
			}
			return []string{sourceRefKey(obj).String()}
		}); err != nil {
		return fmt.Errorf("could not index ImageBuilds by source: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&buildv1alpha1.ImageBuild{}, builder.WithPredicates(
			predicate.Or(predicate.GenerationChangedPredicate{}, predicates.ReconcileRequestedPredicate{}),
		)).
		Owns(&batchv1.Job{}).
		Watches(&sourcev1.GitRepository{},
			handler.EnqueueRequestsFromMapFunc(r.imageBuildsForSource),
			builder.WithPredicates(SourceArtifactChangedPredicate{}),
		).
		// A finished build pod holds the result, and Job status can trail it.
		Watches(&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.imageBuildForPod),
			builder.WithPredicates(buildPodFinishedPredicate()),
		).
		Named("imagebuild").
		Complete(r)
}

// imageBuildForPod maps a build pod to the ImageBuild that controls its Job.
func (r *ImageBuildReconciler) imageBuildForPod(ctx context.Context, o client.Object) []ctrl.Request {
	owner := metav1.GetControllerOf(o)
	if owner == nil || owner.Kind != "Job" {
		return nil
	}
	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: o.GetNamespace(), Name: owner.Name}, job); err != nil {
		return nil
	}
	key, ok := imageBuildForJob(job)
	if !ok {
		return nil
	}
	return []ctrl.Request{{NamespacedName: key}}
}

// buildPodFinishedPredicate passes build pods that finished or are being deleted.
func buildPodFinishedPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(o client.Object) bool {
		pod, ok := o.(*corev1.Pod)
		if !ok || pod.Labels[build.ManagedByLabel] != build.ManagedByValue {
			return false
		}
		return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed ||
			!pod.DeletionTimestamp.IsZero()
	})
}

// imageBuildsForSource maps a GitRepository to the ImageBuilds that reference it.
func (r *ImageBuildReconciler) imageBuildsForSource(ctx context.Context, o client.Object) []ctrl.Request {
	var list buildv1alpha1.ImageBuildList
	key := types.NamespacedName{Namespace: o.GetNamespace(), Name: o.GetName()}.String()
	if err := r.List(ctx, &list, client.MatchingFields{sourceRefIndexKey: key}); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list ImageBuilds for GitRepository", "gitrepository", key)
		return nil
	}
	requests := make([]ctrl.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return requests
}

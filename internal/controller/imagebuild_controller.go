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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/events"
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
)

const (
	// ControllerName is the field owner for status patches and the name on recorded Events.
	ControllerName = "image-build-controller"

	// sourceRefIndexKey indexes ImageBuilds by the namespace/name of the GitRepository they reference.
	sourceRefIndexKey = ".spec.sourceRef"

	// BuildNotImplementedReason means the source is ready, but this controller version can't run builds yet.
	// It is temporary and goes away once the controller creates build Jobs.
	BuildNotImplementedReason = "BuildNotImplemented"
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
}

// +kubebuilder:rbac:groups=build.fluxcd.leightweight.io,resources=imagebuilds,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=build.fluxcd.leightweight.io,resources=imagebuilds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile resolves the ImageBuild's source and records the result in its status.
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
		// A reconcile request counts as handled even when the ImageBuild is suspended.
		if token, ok := meta.ReconcileAnnotationValue(obj.GetAnnotations()); ok {
			obj.Status.SetLastHandledReconcileRequest(token)
		}
		patchOpts := reconcile.AddPatchOptions(obj, nil, ownedConditions, ControllerName)
		if err := serialPatcher.Patch(ctx, obj, patchOpts...); err != nil {
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

	return r.reconcile(ctx, obj)
}

// reconcile does the work for a non-suspended ImageBuild and finalizes its conditions to follow kstatus.
func (r *ImageBuildReconciler) reconcile(ctx context.Context, obj *buildv1alpha1.ImageBuild) (result ctrl.Result, retErr error) {
	interval := obj.Spec.Interval.Duration
	sourceReady := false

	defer func() {
		// A reconciliation succeeds when the source is ready and the next check is scheduled for spec.interval.
		isSuccess := func(res ctrl.Result, err error) bool {
			return err == nil && sourceReady && res.RequeueAfter == interval
		}
		rs := reconcile.NewResultFinalizer(isSuccess, "ImageBuild is ready")
		retErr = rs.Finalize(obj, result, retErr)

		// A Reconciling condition left after finalizing means this reconciliation will be retried.
		if conditions.IsReconciling(obj) {
			reconciling := conditions.Get(obj, meta.ReconcilingCondition)
			reconciling.Reason = meta.ProgressingWithRetryReason
			conditions.Set(obj, reconciling)
		}
	}()

	reconcile.ProgressiveStatus(false, obj, meta.ProgressingReason, "Reconciliation in progress")

	if obj.Spec.SourceRef != nil {
		ready, err := r.reconcileSource(ctx, obj)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !ready {
			// A stalled ImageBuild waits for a spec change instead of retrying.
			if conditions.IsStalled(obj) {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{RequeueAfter: interval}, nil
		}
	}
	sourceReady = true

	// Builds come in a later version of the controller.
	// Until then, report that this controller can't make progress without being upgraded.
	conditions.MarkStalled(obj, BuildNotImplementedReason, "Building images is not implemented yet")
	conditions.MarkFalse(obj, meta.ReadyCondition, BuildNotImplementedReason, "Building images is not implemented yet")
	return ctrl.Result{}, nil
}

// reconcileSource checks that the referenced GitRepository has an artifact to build from.
// It returns false with conditions set when the build has to wait for the source.
func (r *ImageBuildReconciler) reconcileSource(ctx context.Context, obj *buildv1alpha1.ImageBuild) (bool, error) {
	ref := sourceRefKey(obj)

	if r.NoCrossNamespaceRefs && ref.Namespace != obj.Namespace {
		conditions.MarkStalled(obj, aclapi.AccessDeniedReason,
			"Cross-namespace references are not allowed: %s %s", obj.Spec.SourceRef.Kind, ref)
		conditions.MarkFalse(obj, meta.ReadyCondition, aclapi.AccessDeniedReason,
			"Cross-namespace references are not allowed: %s %s", obj.Spec.SourceRef.Kind, ref)
		return false, nil
	}

	source := &sourcev1.GitRepository{}
	if err := r.Get(ctx, ref, source); err != nil {
		if apierrors.IsNotFound(err) {
			markSourceNotReady(obj, "GitRepository %s was not found", ref)
			return false, nil
		}
		return false, fmt.Errorf("could not get GitRepository %s: %w", ref, err)
	}

	// Like the Flux controllers, build from the last artifact even if the source's latest fetch failed.
	if source.GetArtifact() == nil {
		markSourceNotReady(obj, "GitRepository %s has no artifact yet", ref)
		return false, nil
	}
	return true, nil
}

func markSourceNotReady(obj *buildv1alpha1.ImageBuild, messageFormat string, args ...any) {
	conditions.MarkReconciling(obj, buildv1alpha1.SourceNotReadyReason, messageFormat, args...)
	conditions.MarkFalse(obj, meta.ReadyCondition, buildv1alpha1.SourceNotReadyReason, messageFormat, args...)
}

// recordReadyChange records an Event when the Ready condition's status or reason changes.
func (r *ImageBuildReconciler) recordReadyChange(obj *buildv1alpha1.ImageBuild, oldReady *metav1.Condition) {
	if r.EventRecorder == nil {
		return
	}
	newReady := conditions.Get(obj, meta.ReadyCondition)
	if newReady == nil {
		return
	}
	if oldReady != nil && oldReady.Status == newReady.Status && oldReady.Reason == newReady.Reason {
		return
	}
	eventType := corev1.EventTypeNormal
	if newReady.Status != metav1.ConditionTrue {
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
		Watches(&sourcev1.GitRepository{},
			handler.EnqueueRequestsFromMapFunc(r.imageBuildsForSource),
			builder.WithPredicates(SourceArtifactChangedPredicate{}),
		).
		Named("imagebuild").
		Complete(r)
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

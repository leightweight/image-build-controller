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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	buildv1alpha1 "github.com/leightweight/image-build-controller/api/v1alpha1"
	"github.com/leightweight/image-build-controller/internal/build"
)

// BuildPodReconciler releases the termination message finalizer from build pods that nothing will read anymore:
// pods being deleted whose Job or ImageBuild is gone or being deleted.
// The ImageBuild reconciler releases the pods of Jobs whose results it recorded.
type BuildPodReconciler struct {
	client.Client
}

// Reconcile releases a deleted build pod when its result no longer has a reader.
func (r *BuildPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pod := &corev1.Pod{}
	if err := r.Get(ctx, req.NamespacedName, pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if pod.DeletionTimestamp.IsZero() || !controllerutil.ContainsFinalizer(pod, build.TerminationMessageFinalizer) {
		return ctrl.Result{}, nil
	}

	orphaned, err := r.orphaned(ctx, pod)
	if err != nil || !orphaned {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, releasePod(ctx, r.Client, pod)
}

// orphaned reports whether no ImageBuild will record the pod's result.
func (r *BuildPodReconciler) orphaned(ctx context.Context, pod *corev1.Pod) (bool, error) {
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.Kind != "Job" {
		return true, nil
	}
	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: owner.Name}, job); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("could not get Job %s: %w", owner.Name, err)
	}
	if job.UID != owner.UID || jobDeleting(job) {
		return true, nil
	}

	key, ok := imageBuildForJob(job)
	if !ok {
		return true, nil
	}
	obj := &buildv1alpha1.ImageBuild{}
	if err := r.Get(ctx, key, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("could not get ImageBuild %s: %w", key, err)
	}
	return !obj.DeletionTimestamp.IsZero(), nil
}

// podsForJob maps a build Job to its pods that wait for their finalizer to be released.
func (r *BuildPodReconciler) podsForJob(ctx context.Context, o client.Object) []ctrl.Request {
	job, ok := o.(*batchv1.Job)
	if !ok {
		return nil
	}
	pods, err := jobPods(ctx, r.Client, job)
	if err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list pods for Job", "job", klog.KObj(job))
		return nil
	}
	var requests []ctrl.Request
	for i := range pods {
		if !pods[i].DeletionTimestamp.IsZero() {
			requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&pods[i])})
		}
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *BuildPodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
			return o.GetLabels()[build.ManagedByLabel] == build.ManagedByValue &&
				!o.GetDeletionTimestamp().IsZero() &&
				controllerutil.ContainsFinalizer(o, build.TerminationMessageFinalizer)
		}))).
		// A pod that was already being deleted gets no new event when its Job goes away, so watch Jobs too.
		Watches(&batchv1.Job{}, handler.EnqueueRequestsFromMapFunc(r.podsForJob), builder.WithPredicates(
			predicate.NewPredicateFuncs(func(o client.Object) bool {
				return o.GetLabels()[build.ManagedByLabel] == build.ManagedByValue
			}),
		)).
		Named("buildpod").
		Complete(r)
}

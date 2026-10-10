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
	"regexp"
	"slices"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	buildv1alpha1 "github.com/leightweight/image-build-controller/api/v1alpha1"
	"github.com/leightweight/image-build-controller/internal/build"
)

// digestPattern matches the image digest a successful build writes as its termination message.
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// buildLabels selects the build Jobs and pods the controller creates.
var buildLabels = client.MatchingLabels{build.ManagedByLabel: build.ManagedByValue}

// jobOutcome is what a finished build Job produced.
type jobOutcome struct {
	succeeded bool
	// digest is the pushed image digest after a successful build, if the build reported one.
	digest string
	// message is the termination message of the failed attempt, usually the tail of its log.
	message    string
	finishedAt metav1.Time
}

// ownedJobs lists the build Jobs the ImageBuild controls, oldest first.
func (r *ImageBuildReconciler) ownedJobs(ctx context.Context, obj *buildv1alpha1.ImageBuild) ([]batchv1.Job, error) {
	var list batchv1.JobList
	if err := r.List(ctx, &list, client.InNamespace(obj.Namespace), buildLabels); err != nil {
		return nil, fmt.Errorf("could not list build Jobs: %w", err)
	}
	jobs := slices.DeleteFunc(list.Items, func(job batchv1.Job) bool {
		owner := metav1.GetControllerOf(&job)
		return owner == nil || owner.UID != obj.UID
	})
	slices.SortStableFunc(jobs, func(a, b batchv1.Job) int {
		return a.CreationTimestamp.Compare(b.CreationTimestamp.Time)
	})
	return jobs, nil
}

// jobPods lists the pods a build Job controls, oldest first.
func jobPods(ctx context.Context, c client.Reader, job *batchv1.Job) ([]corev1.Pod, error) {
	var list corev1.PodList
	if err := c.List(ctx, &list, client.InNamespace(job.Namespace), buildLabels); err != nil {
		return nil, fmt.Errorf("could not list pods of Job %s: %w", job.Name, err)
	}
	pods := slices.DeleteFunc(list.Items, func(pod corev1.Pod) bool {
		owner := metav1.GetControllerOf(&pod)
		return owner == nil || owner.UID != job.UID
	})
	slices.SortStableFunc(pods, func(a, b corev1.Pod) int {
		return a.CreationTimestamp.Compare(b.CreationTimestamp.Time)
	})
	return pods, nil
}

// jobFinished reports whether a Job completed or failed, and when.
func jobFinished(job *batchv1.Job) (finished, succeeded bool, at metav1.Time) {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return true, true, c.LastTransitionTime
		case batchv1.JobFailed:
			return true, false, c.LastTransitionTime
		}
	}
	return false, false, metav1.Time{}
}

func jobKey(job *batchv1.Job) string {
	return job.Annotations[build.BuildKeyAnnotation]
}

// jobDeleting reports whether a Job is being deleted.
func jobDeleting(job *batchv1.Job) bool {
	return !job.DeletionTimestamp.IsZero()
}

// recorded reports whether the ImageBuild's lastBuild already describes the Job.
func recorded(obj *buildv1alpha1.ImageBuild, job *batchv1.Job) bool {
	last := obj.Status.LastBuild
	return last != nil && last.JobName == job.Name && last.BuildKey == jobKey(job)
}

// pending reports whether a Job is the build the status names as current, so its result still needs recording.
// Every other finished Job is history, even if the build key matches it again.
func pending(obj *buildv1alpha1.ImageBuild, job *batchv1.Job) bool {
	cb := obj.Status.CurrentBuild
	return cb != nil && cb.JobName == job.Name && !recorded(obj, job)
}

// outcome reads what a finished build Job produced from its pods' termination messages.
func (r *ImageBuildReconciler) outcome(ctx context.Context, job *batchv1.Job) (jobOutcome, error) {
	_, succeeded, finishedAt := jobFinished(job)
	out := jobOutcome{succeeded: succeeded, finishedAt: finishedAt}

	pods, err := jobPods(ctx, r.Client, job)
	if err != nil {
		return out, err
	}

	if succeeded {
		for _, pod := range slices.Backward(pods) {
			if pod.Status.Phase != corev1.PodSucceeded {
				continue
			}
			message := strings.TrimSpace(terminationMessage(pod.Status.ContainerStatuses, build.BuildContainerName))
			if digestPattern.MatchString(message) {
				out.digest = message
			}
			break
		}
		return out, nil
	}

	// The last attempt to finish explains the failure. Earlier attempts usually failed the same way.
	slices.SortStableFunc(pods, func(a, b corev1.Pod) int {
		return podFinishedAt(&a).Compare(podFinishedAt(&b).Time)
	})
	for i := len(pods) - 1; i >= 0 && out.message == ""; i-- {
		out.message = failureMessage(&pods[i])
	}
	if out.message == "" {
		for _, c := range job.Status.Conditions {
			if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
				out.message = strings.TrimSpace(c.Reason + ": " + c.Message)
			}
		}
	}
	return out, nil
}

// podFinishedAt returns when a pod's last container finished, or its creation time if none has.
func podFinishedAt(pod *corev1.Pod) metav1.Time {
	at := pod.CreationTimestamp
	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses} {
		for _, s := range statuses {
			if t := s.State.Terminated; t != nil && t.FinishedAt.After(at.Time) {
				at = t.FinishedAt
			}
		}
	}
	return at
}

// failureMessage returns the termination message of the container that failed, if any.
// A failed fetch stops the pod before the build container starts, so it checks the init container too.
func failureMessage(pod *corev1.Pod) string {
	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses} {
		for _, s := range statuses {
			if t := s.State.Terminated; t != nil && t.ExitCode != 0 {
				if message := strings.TrimSpace(t.Message); message != "" {
					return message
				}
			}
		}
	}
	return ""
}

func terminationMessage(statuses []corev1.ContainerStatus, container string) string {
	for _, s := range statuses {
		if s.Name == container && s.State.Terminated != nil {
			return s.State.Terminated.Message
		}
	}
	return ""
}

// releasePods removes the termination message finalizer from a Job's finished pods.
func releasePods(ctx context.Context, c client.Client, job *batchv1.Job) error {
	pods, err := jobPods(ctx, c, job)
	if err != nil {
		return err
	}
	for i := range pods {
		pod := &pods[i]
		finished := pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
		if !finished && pod.DeletionTimestamp.IsZero() {
			continue
		}
		if err := releasePod(ctx, c, pod); err != nil {
			return err
		}
	}
	return nil
}

// releasePod removes the termination message finalizer from a pod.
// The patch carries the pod's resourceVersion, so it can't drop a finalizer another controller just added.
func releasePod(ctx context.Context, c client.Client, pod *corev1.Pod) error {
	if !controllerutil.ContainsFinalizer(pod, build.TerminationMessageFinalizer) {
		return nil
	}
	base := pod.DeepCopy()
	controllerutil.RemoveFinalizer(pod, build.TerminationMessageFinalizer)
	if err := c.Patch(ctx, pod, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("could not release pod %s: %w", pod.Name, err)
	}
	return nil
}

// deleteJob deletes a build Job in the background. Its pods are deleted afterwards,
// and the build pod reconciler releases their finalizers.
func deleteJob(ctx context.Context, c client.Client, job *batchv1.Job) error {
	err := c.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground),
		client.Preconditions{UID: &job.UID})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("could not delete Job %s: %w", job.Name, err)
	}
	return nil
}

// imageBuildForJob returns the ImageBuild that controls a Job, if any.
func imageBuildForJob(job *batchv1.Job) (types.NamespacedName, bool) {
	owner := metav1.GetControllerOf(job)
	if owner == nil || owner.Kind != buildv1alpha1.ImageBuildKind ||
		!strings.HasPrefix(owner.APIVersion, buildv1alpha1.SchemeGroupVersion.Group+"/") {
		return types.NamespacedName{}, false
	}
	return types.NamespacedName{Namespace: job.Namespace, Name: owner.Name}, true
}

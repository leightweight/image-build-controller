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
	"slices"
	"strings"
	"time"

	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	buildv1alpha1 "github.com/leightweight/image-build-controller/api/v1alpha1"
	"github.com/leightweight/image-build-controller/internal/build"
)

// envtest runs no kube-controller-manager, so these helpers play the Job controller and the kubelet:
// they create a Job's pods, finish them with a termination message, and set the Job's final status.

const (
	testDigest = "sha256:0d1e2c3b4a5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"
	// testPodImage is the image of the stand-in build pods; no kubelet ever pulls it.
	testPodImage = "busybox"
	testToken    = "token-1"
	testTarget   = "runtime"
)

// listJobs returns the build Jobs in a namespace that aren't being deleted, oldest first.
func listJobs(ctx context.Context, namespace string) []batchv1.Job {
	var list batchv1.JobList
	Expect(k8sClient.List(ctx, &list, client.InNamespace(namespace))).To(Succeed())
	var jobs []batchv1.Job
	for _, job := range list.Items {
		if job.DeletionTimestamp.IsZero() {
			jobs = append(jobs, job)
		}
	}
	return jobs
}

// onlyJob returns the namespace's single build Job.
func onlyJob(ctx context.Context, namespace string) *batchv1.Job {
	jobs := listJobs(ctx, namespace)
	ExpectWithOffset(1, jobs).To(HaveLen(1))
	return &jobs[0]
}

// addPod creates a build pod for a Job, with the template's labels and finalizer, the way the Job controller does.
func addPod(ctx context.Context, job *batchv1.Job) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       job.Namespace,
			GenerateName:    job.Name + "-",
			Labels:          job.Spec.Template.Labels,
			Finalizers:      job.Spec.Template.Finalizers,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(job, batchv1.SchemeGroupVersion.WithKind("Job"))},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers:    []corev1.Container{{Name: build.BuildContainerName, Image: testPodImage}},
		},
	}
	Expect(k8sClient.Create(ctx, pod)).To(Succeed())
	return pod
}

// podsTerminated counts terminated pods, so each one finishes a second after the one before,
// like Job retries, which are at least 10 seconds apart.
var podsTerminated int

// terminatePod finishes a pod's build container with an exit code and termination message.
func terminatePod(ctx context.Context, pod *corev1.Pod, exitCode int32, message string) {
	podsTerminated++
	finishedAt := metav1.NewTime(time.Now().Add(time.Duration(podsTerminated) * time.Second))
	pod.Status.Phase = corev1.PodSucceeded
	if exitCode != 0 {
		pod.Status.Phase = corev1.PodFailed
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  build.BuildContainerName,
		Image: testPodImage,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode: exitCode, Message: message, FinishedAt: finishedAt,
		}},
	}}
	Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
}

// succeedJob runs one successful pod that reports digest, and marks the Job complete.
func succeedJob(ctx context.Context, job *batchv1.Job, digest string) *corev1.Pod {
	pod := addPod(ctx, job)
	terminatePod(ctx, pod, 0, digest)
	setJobFinished(ctx, job, true, 1)
	return pod
}

// failJob runs one failed pod per message, the last one being the final attempt, and marks the Job failed.
func failJob(ctx context.Context, job *batchv1.Job, messages ...string) []*corev1.Pod {
	pods := make([]*corev1.Pod, 0, len(messages))
	for _, message := range messages {
		pod := addPod(ctx, job)
		terminatePod(ctx, pod, 1, message)
		pods = append(pods, pod)
	}
	setJobFinished(ctx, job, false, int32(len(messages)))
	return pods
}

// setJobFinished sets the conditions the Job controller sets when a Job completes or fails.
func setJobFinished(ctx context.Context, job *batchv1.Job, succeeded bool, pods int32) {
	ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(Succeed())
	now := metav1.Now()
	job.Status.StartTime = &now
	if succeeded {
		job.Status.Succeeded = pods
		job.Status.CompletionTime = &now
		job.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: now},
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: now},
		}
	} else {
		job.Status.Failed = pods
		job.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, LastTransitionTime: now,
				Reason: batchv1.JobReasonBackoffLimitExceeded, Message: "Job has reached the specified backoff limit"},
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, LastTransitionTime: now,
				Reason: batchv1.JobReasonBackoffLimitExceeded, Message: "Job has reached the specified backoff limit"},
		}
	}
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, job)).To(Succeed())
}

// hasFinalizer reports whether a pod still holds the termination message finalizer, treating a deleted pod as released.
func hasFinalizer(ctx context.Context, pod *corev1.Pod) bool {
	got := &corev1.Pod{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), got); err != nil {
		return false
	}
	return slices.Contains(got.Finalizers, build.TerminationMessageFinalizer)
}

// requestReconcile sets a new reconcile.fluxcd.io/requestedAt token, which forces a new build.
func requestReconcile(ctx context.Context, ib *buildv1alpha1.ImageBuild, token string) {
	got := getImageBuild(ctx, ib)
	if got.Annotations == nil {
		got.Annotations = map[string]string{}
	}
	got.Annotations["reconcile.fluxcd.io/requestedAt"] = token
	Expect(k8sClient.Update(ctx, got)).To(Succeed())
}

// updateSpec changes an ImageBuild's spec.
func updateSpec(ctx context.Context, ib *buildv1alpha1.ImageBuild, mutate func(*buildv1alpha1.ImageBuild)) {
	got := getImageBuild(ctx, ib)
	mutate(got)
	Expect(k8sClient.Update(ctx, got)).To(Succeed())
}

// jobNames returns the names of Jobs.
func jobNames(jobs []batchv1.Job) string {
	names := make([]string, 0, len(jobs))
	for _, job := range jobs {
		names = append(names, job.Name)
	}
	return strings.Join(names, ",")
}

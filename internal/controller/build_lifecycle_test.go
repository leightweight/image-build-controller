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
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/conditions"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	buildv1alpha1 "github.com/leightweight/image-build-controller/api/v1alpha1"
	"github.com/leightweight/image-build-controller/internal/build"
)

var _ = Describe("Build lifecycle", func() {
	var (
		ctx        context.Context
		namespace  string
		recorder   *events.FakeRecorder
		reconciler *ImageBuildReconciler
		now        time.Time
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespace = newNamespace(ctx)
		recorder = events.NewFakeRecorder(20)
		now = testNow
		reconciler = &ImageBuildReconciler{
			Client:        k8sClient,
			EventRecorder: recorder,
			BuildConfig:   testBuildConfig,
			Now:           func() time.Time { return now },
		}
	})

	reconcileOnce := func(ib *buildv1alpha1.ImageBuild) ctrl.Result {
		GinkgoHelper()
		result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ib)})
		Expect(err).NotTo(HaveOccurred())
		return result
	}

	// startNext reconciles and returns the one Job the reconciliation created.
	startNext := func(ib *buildv1alpha1.ImageBuild) *batchv1.Job {
		GinkgoHelper()
		before := map[types.UID]bool{}
		for _, job := range listJobs(ctx, namespace) {
			before[job.UID] = true
		}
		reconcileOnce(ib)
		var created []batchv1.Job
		for _, job := range listJobs(ctx, namespace) {
			if !before[job.UID] {
				created = append(created, job)
			}
		}
		Expect(created).To(HaveLen(1))
		return &created[0]
	}

	// buildOnce starts a build and finishes it successfully.
	buildOnce := func(ib *buildv1alpha1.ImageBuild) *batchv1.Job {
		GinkgoHelper()
		job := startNext(ib)
		succeedJob(ctx, job, testDigest)
		reconcileOnce(ib)
		return job
	}

	Context("starting a build", func() {
		It("creates one build Job for the ImageBuild and reports it as running", func() {
			ib := newImageBuild(ctx, namespace, nil)

			result := reconcileOnce(ib)

			job := onlyJob(ctx, namespace)
			got := getImageBuild(ctx, ib)
			Expect(job.Name).To(Equal(build.JobName(ib.Name, build.Key(got, nil, now))))
			Expect(job.Labels).To(HaveKeyWithValue(build.ManagedByLabel, build.ManagedByValue))
			Expect(job.Spec.Template.Finalizers).To(ContainElement(build.TerminationMessageFinalizer))
			Expect(metav1.IsControlledBy(job, got)).To(BeTrue())
			Expect(got.Status.CurrentBuild).NotTo(BeNil())
			Expect(got.Status.CurrentBuild.JobName).To(Equal(job.Name))
			Expect(got.Status.CurrentBuild.StartedAt).NotTo(BeNil())
			Expect(got).To(haveCondition(meta.ReconcilingCondition, metav1.ConditionTrue, buildv1alpha1.BuildingReason))
			Expect(got).To(haveCondition(meta.ReadyCondition, metav1.ConditionUnknown, buildv1alpha1.BuildingReason))
			Expect(got).To(notHaveCondition(meta.StalledCondition))
			Expect(result.RequeueAfter).To(Equal(defaultInterval))
			Expect(recorder.Events).To(Receive(HavePrefix("Normal " + buildv1alpha1.BuildingReason)))

			reconcileOnce(ib)
			Expect(listJobs(ctx, namespace)).To(HaveLen(1), "a running build isn't started again")
		})

		It("adopts the running Job after a restart instead of starting another", func() {
			ib := newImageBuild(ctx, namespace, nil)
			reconcileOnce(ib)
			job := onlyJob(ctx, namespace)

			// A controller that crashed after creating the Job never recorded it.
			got := getImageBuild(ctx, ib)
			got.Status.CurrentBuild = nil
			Expect(k8sClient.Status().Update(ctx, got)).To(Succeed())
			reconcileOnce(ib)

			Expect(onlyJob(ctx, namespace).UID).To(Equal(job.UID))
			Expect(getImageBuild(ctx, ib).Status.CurrentBuild.JobName).To(Equal(job.Name))
		})

		It("fetches the source with the configured fetch image and tags git tag builds with the tag", func() {
			repo := newGitRepository(ctx, namespace, "app", "")
			repo.Spec.Reference = &sourcev1.GitRepositoryRef{Tag: "v1.2.3"}
			Expect(k8sClient.Update(ctx, repo)).To(Succeed())
			setArtifact(ctx, repo, "v1.2.3@sha1:0123456789abcdef0123456789abcdef01234567")
			ib := newImageBuild(ctx, namespace, gitRepositoryRef("", "app"))

			reconcileOnce(ib)

			job := onlyJob(ctx, namespace)
			Expect(job.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			Expect(job.Spec.Template.Spec.InitContainers[0].Image).To(Equal(testBuildConfig.FetchImage))
			Expect(job.Annotations).To(HaveKeyWithValue(build.TagsAnnotation,
				"v1.2.3-0123456-"+timestamp(now)))
			Expect(getImageBuild(ctx, ib).Status.CurrentBuild.SourceRevision).To(
				Equal("v1.2.3@sha1:0123456789abcdef0123456789abcdef01234567"))
		})

		DescribeTable("runs builds in user namespaces unless turned off",
			func(defaultUserNamespaces bool, override *bool, wantHostUsers *bool) {
				reconciler.BuildConfig.DefaultUserNamespaces = defaultUserNamespaces
				ib := newImageBuild(ctx, namespace, nil, func(ib *buildv1alpha1.ImageBuild) {
					ib.Spec.UserNamespaces = override
				})

				reconcileOnce(ib)

				Expect(onlyJob(ctx, namespace).Spec.Template.Spec.HostUsers).To(Equal(wantHostUsers))
			},
			Entry("controller default on", true, nil, new(false)),
			Entry("controller default off", false, nil, nil),
			Entry("turned off for one ImageBuild", true, new(false), nil),
			Entry("turned on for one ImageBuild", false, new(true), new(false)),
		)

		It("stalls when the tag templates can't be rendered", func() {
			ib := newImageBuild(ctx, namespace, nil, func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.Push.Tags = []string{"{{ .Unknown }}"}
			})

			result := reconcileOnce(ib)

			Expect(result).To(BeZero())
			Expect(listJobs(ctx, namespace)).To(BeEmpty())
			got := getImageBuild(ctx, ib)
			Expect(got).To(haveCondition(meta.StalledCondition, metav1.ConditionTrue, buildv1alpha1.InvalidTagsReason))
			Expect(got).To(haveCondition(meta.ReadyCondition, metav1.ConditionFalse, buildv1alpha1.InvalidTagsReason))
		})
	})

	Context("when a build succeeds", func() {
		It("records the pushed image and releases the pod", func() {
			ib := newImageBuild(ctx, namespace, nil)
			reconcileOnce(ib)
			job := onlyJob(ctx, namespace)
			pod := succeedJob(ctx, job, testDigest)

			result := reconcileOnce(ib)

			got := getImageBuild(ctx, ib)
			Expect(got.Status.CurrentBuild).To(BeNil())
			Expect(got.Status.LastBuild).NotTo(BeNil())
			Expect(got.Status.LastBuild.Result).To(Equal(buildv1alpha1.BuildSucceeded))
			Expect(got.Status.LastBuild.BuildKey).To(Equal(job.Annotations[build.BuildKeyAnnotation]))
			Expect(got.Status.LastBuild.JobName).To(Equal(job.Name))
			Expect(got.Status.LastBuild.Image.Digest).To(Equal(testDigest))
			Expect(got.Status.LastBuild.Image.Tags).To(Equal([]string{"build-" + timestamp(now)}))
			Expect(got.Status.LastBuild.StartedAt).NotTo(BeNil())
			Expect(got.Status.LastBuild.FinishedAt).NotTo(BeNil())
			Expect(got.Status.LatestImage).To(Equal("registry.example/app@" + testDigest))
			Expect(got).To(haveCondition(meta.ReadyCondition, metav1.ConditionTrue, meta.SucceededReason))
			Expect(conditions.GetMessage(got, meta.ReadyCondition)).To(Equal("Pushed registry.example/app@" + testDigest))
			Expect(got).To(notHaveCondition(meta.ReconcilingCondition))
			Expect(got).To(notHaveCondition(meta.StalledCondition))
			Expect(got.Status.ObservedGeneration).To(Equal(got.Generation))
			Expect(result.RequeueAfter).To(Equal(defaultInterval))
			Expect(hasFinalizer(ctx, pod)).To(BeFalse())
			Expect(recorder.Events).To(Receive(HavePrefix("Normal " + buildv1alpha1.BuildingReason)))
			Expect(recorder.Events).To(Receive(Equal("Normal " + meta.SucceededReason + " Pushed registry.example/app@" + testDigest)))
		})

		It("doesn't build again while nothing changes", func() {
			ib := newImageBuild(ctx, namespace, nil)
			buildOnce(ib)

			reconcileOnce(ib)
			reconcileOnce(ib)

			Expect(listJobs(ctx, namespace)).To(HaveLen(1))
			Expect(getImageBuild(ctx, ib)).To(haveCondition(meta.ReadyCondition, metav1.ConditionTrue, meta.SucceededReason))
		})

		It("builds again when the source has a new artifact", func() {
			newGitRepository(ctx, namespace, "app", "main@sha1:0123456789abcdef0123456789abcdef01234567")
			ib := newImageBuild(ctx, namespace, gitRepositoryRef("", "app"))
			first := buildOnce(ib)

			repo := &sourcev1.GitRepository{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "app"}, repo)).To(Succeed())
			repo.Status.Artifact.Revision = "main@sha1:89abcdef0123456789abcdef0123456789abcdef"
			repo.Status.Artifact.Digest = "sha256:" + "1111111111111111111111111111111111111111111111111111111111111111"
			Expect(k8sClient.Status().Update(ctx, repo)).To(Succeed())
			reconcileOnce(ib)

			jobs := listJobs(ctx, namespace)
			Expect(jobs).To(HaveLen(2), jobNames(jobs))
			Expect(jobs[1].Name).NotTo(Equal(first.Name))
			Expect(jobs[1].Annotations).To(HaveKeyWithValue(build.SourceRevisionAnnotation,
				"main@sha1:89abcdef0123456789abcdef0123456789abcdef"))
		})

		It("builds again when reconcile.fluxcd.io/requestedAt changes", func() {
			ib := newImageBuild(ctx, namespace, nil)
			first := buildOnce(ib)

			requestReconcile(ctx, ib, testToken)
			reconcileOnce(ib)

			jobs := listJobs(ctx, namespace)
			Expect(jobs).To(HaveLen(2), jobNames(jobs))
			Expect(jobs[1].Name).NotTo(Equal(first.Name))
		})

		It("reports an image tag when the build didn't report a digest", func() {
			ib := newImageBuild(ctx, namespace, nil)
			reconcileOnce(ib)
			succeedJob(ctx, onlyJob(ctx, namespace), "")

			reconcileOnce(ib)

			got := getImageBuild(ctx, ib)
			Expect(got.Status.LastBuild.Image.Digest).To(BeEmpty())
			Expect(got.Status.LatestImage).To(Equal("registry.example/app:build-" + timestamp(now)))
		})
	})

	Context("when a build fails", func() {
		It("stalls with the end of the build log and doesn't retry the same build", func() {
			ib := newImageBuild(ctx, namespace, nil)
			reconcileOnce(ib)
			job := onlyJob(ctx, namespace)
			pods := failJob(ctx, job, "ERROR: failed to solve: process \"/bin/sh -c make\" did not complete successfully")

			result := reconcileOnce(ib)

			got := getImageBuild(ctx, ib)
			Expect(got.Status.CurrentBuild).To(BeNil())
			Expect(got.Status.LastBuild.Result).To(Equal(buildv1alpha1.BuildFailed))
			Expect(got.Status.LastBuild.Image).To(BeNil())
			Expect(got.Status.LatestImage).To(BeEmpty())
			Expect(got).To(haveCondition(meta.StalledCondition, metav1.ConditionTrue, buildv1alpha1.BuildFailedReason))
			Expect(got).To(haveCondition(meta.ReadyCondition, metav1.ConditionFalse, buildv1alpha1.BuildFailedReason))
			Expect(conditions.GetMessage(got, meta.ReadyCondition)).To(Equal(
				"Build Job " + job.Name + " failed: ERROR: failed to solve: process \"/bin/sh -c make\" did not complete successfully"))
			Expect(got).To(notHaveCondition(meta.ReconcilingCondition))
			Expect(result.RequeueAfter).To(Equal(defaultInterval))
			Expect(hasFinalizer(ctx, pods[0])).To(BeFalse())
			Expect(recorder.Events).To(Receive(HavePrefix("Normal " + buildv1alpha1.BuildingReason)))
			Expect(recorder.Events).To(Receive(HavePrefix("Warning " + buildv1alpha1.BuildFailedReason + " Build Job " + job.Name)))

			reconcileOnce(ib)

			Expect(listJobs(ctx, namespace)).To(HaveLen(1), "a failed build key isn't retried")
			Expect(conditions.GetMessage(getImageBuild(ctx, ib), meta.ReadyCondition)).To(ContainSubstring("failed to solve"),
				"the failure message survives later reconciliations")
		})

		It("uses the last attempt's message and releases every attempt's pod", func() {
			ib := newImageBuild(ctx, namespace, nil)
			reconcileOnce(ib)
			pods := failJob(ctx, onlyJob(ctx, namespace), "attempt 1: timeout", "attempt 2: timeout", "attempt 3: denied")

			reconcileOnce(ib)

			Expect(conditions.GetMessage(getImageBuild(ctx, ib), meta.ReadyCondition)).To(HaveSuffix("attempt 3: denied"))
			for _, pod := range pods {
				Expect(hasFinalizer(ctx, pod)).To(BeFalse())
			}
		})

		It("keeps the full log tail in the condition but shortens the Event to what the API server accepts", func() {
			ib := newImageBuild(ctx, namespace, nil)
			reconcileOnce(ib)
			tail := strings.Repeat("#7 0.123 compiling...\n", 100) + "ERROR: failed to solve"
			failJob(ctx, onlyJob(ctx, namespace), tail)

			reconcileOnce(ib)

			Expect(conditions.GetMessage(getImageBuild(ctx, ib), meta.ReadyCondition)).To(HaveSuffix(tail))
			Expect(recorder.Events).To(Receive(HavePrefix("Normal " + buildv1alpha1.BuildingReason)))
			var event string
			Expect(recorder.Events).To(Receive(&event))
			note := strings.TrimPrefix(event, "Warning "+buildv1alpha1.BuildFailedReason+" ")
			Expect(len(note)).To(BeNumerically("<=", maxEventNoteLength))
			Expect(note).To(HavePrefix("..."))
			Expect(note).To(HaveSuffix("ERROR: failed to solve"))
		})

		It("falls back to the Job's reason when the pods left no message", func() {
			ib := newImageBuild(ctx, namespace, nil)
			reconcileOnce(ib)
			failJob(ctx, onlyJob(ctx, namespace), "")

			reconcileOnce(ib)

			Expect(conditions.GetMessage(getImageBuild(ctx, ib), meta.ReadyCondition)).To(HaveSuffix(
				batchv1.JobReasonBackoffLimitExceeded + ": Job has reached the specified backoff limit"))
		})

		It("builds again after a change", func() {
			ib := newImageBuild(ctx, namespace, nil)
			reconcileOnce(ib)
			failJob(ctx, onlyJob(ctx, namespace), "boom")
			reconcileOnce(ib)

			updateSpec(ctx, ib, func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Dockerfile.Inline = "FROM alpine:3.22\n" })
			reconcileOnce(ib)

			Expect(listJobs(ctx, namespace)).To(HaveLen(2))
			got := getImageBuild(ctx, ib)
			Expect(got).To(notHaveCondition(meta.StalledCondition))
			Expect(got).To(haveCondition(meta.ReconcilingCondition, metav1.ConditionTrue, buildv1alpha1.BuildingReason))
		})
	})

	Context("when the build key changes during a build", func() {
		It("replaces the running build by default", func() {
			ib := newImageBuild(ctx, namespace, nil)
			reconcileOnce(ib)
			first := onlyJob(ctx, namespace)

			updateSpec(ctx, ib, func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Target = testTarget })
			reconcileOnce(ib)

			job := onlyJob(ctx, namespace)
			Expect(job.Name).NotTo(Equal(first.Name))
			Expect(getImageBuild(ctx, ib).Status.CurrentBuild.JobName).To(Equal(job.Name))
		})

		It("waits for the running build with concurrencyPolicy Wait, then builds only the latest state", func() {
			ib := newImageBuild(ctx, namespace, nil, func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.ConcurrencyPolicy = buildv1alpha1.WaitConcurrency
			})
			reconcileOnce(ib)
			first := onlyJob(ctx, namespace)

			updateSpec(ctx, ib, func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Target = "one" })
			reconcileOnce(ib)
			updateSpec(ctx, ib, func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Target = "two" })
			reconcileOnce(ib)

			Expect(onlyJob(ctx, namespace).Name).To(Equal(first.Name))
			got := getImageBuild(ctx, ib)
			Expect(got).To(haveCondition(meta.ReconcilingCondition, metav1.ConditionTrue, buildv1alpha1.WaitingForBuildReason))
			Expect(got.Status.CurrentBuild.JobName).To(Equal(first.Name))

			succeedJob(ctx, first, testDigest)
			reconcileOnce(ib)

			got = getImageBuild(ctx, ib)
			Expect(got.Status.LastBuild.JobName).To(Equal(first.Name))
			jobs := listJobs(ctx, namespace)
			Expect(jobs).To(HaveLen(2), jobNames(jobs))
			Expect(jobs[1].Name).To(Equal(build.JobName(ib.Name, build.Key(got, nil, now))),
				"the next build uses the latest spec, skipping the one in between")
			Expect(got.Status.CurrentBuild.JobName).To(Equal(jobs[1].Name))
		})
	})

	Context("history", func() {
		It("keeps one successful Job by default and deletes older ones", func() {
			ib := newImageBuild(ctx, namespace, nil)
			first := buildOnce(ib)
			requestReconcile(ctx, ib, testToken)
			second := buildOnce(ib)

			jobs := listJobs(ctx, namespace)
			Expect(jobs).To(HaveLen(1), jobNames(jobs))
			Expect(jobs[0].Name).To(Equal(second.Name))
			Expect(jobs[0].Name).NotTo(Equal(first.Name))
		})

		It("keeps failed Jobs up to failedBuildsHistoryLimit", func() {
			ib := newImageBuild(ctx, namespace, nil, func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.FailedBuildsHistoryLimit = new(int32(2))
			})
			for i, token := range []string{"", testToken, "token-2"} {
				if token != "" {
					requestReconcile(ctx, ib, token)
				}
				failJob(ctx, startNext(ib), "boom")
				reconcileOnce(ib)
				Expect(len(listJobs(ctx, namespace))).To(BeNumerically("<=", 2), "after failure %d", i+1)
			}
			Expect(listJobs(ctx, namespace)).To(HaveLen(2))
		})

		It("keeps no finished Jobs with limits of 0", func() {
			ib := newImageBuild(ctx, namespace, nil, func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.SuccessfulBuildsHistoryLimit = new(int32(0))
			})
			buildOnce(ib)
			reconcileOnce(ib)

			Expect(listJobs(ctx, namespace)).To(BeEmpty())
			Expect(getImageBuild(ctx, ib).Status.LastBuild.Result).To(Equal(buildv1alpha1.BuildSucceeded))
		})

		It("rebuilds instead of reusing an old Job when the build key returns to an earlier value", func() {
			ib := newImageBuild(ctx, namespace, nil, func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.SuccessfulBuildsHistoryLimit = new(int32(3))
			})
			first := buildOnce(ib)
			updateSpec(ctx, ib, func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Target = testTarget })
			buildOnce(ib)

			updateSpec(ctx, ib, func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Target = "" })
			result := reconcileOnce(ib)

			Expect(result.RequeueAfter).To(Equal(recreateDelay), "the old Job with the same name has to go first")
			Expect(listJobs(ctx, namespace)).NotTo(ContainElement(HaveField("UID", first.UID)))

			reconcileOnce(ib)
			jobs := listJobs(ctx, namespace)
			Expect(jobs).To(ContainElement(HaveField("Name", first.Name)))
			got := getImageBuild(ctx, ib)
			Expect(got.Status.CurrentBuild.JobName).To(Equal(first.Name))
		})
	})

	Context("with rebuildInterval", func() {
		It("checks again just after the current period ends, and rebuilds then", func() {
			ib := newImageBuild(ctx, namespace, nil, func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.RebuildInterval = &metav1.Duration{Duration: time.Hour}
				ib.Spec.Interval = metav1.Duration{Duration: 24 * time.Hour}
			})
			now = time.Date(2026, 10, 10, 12, 15, 0, 0, time.UTC)
			reconcileOnce(ib)
			succeedJob(ctx, onlyJob(ctx, namespace), testDigest)

			result := reconcileOnce(ib)

			Expect(result.RequeueAfter).To(Equal(45*time.Minute + time.Second))

			now = now.Add(45*time.Minute + time.Second)
			reconcileOnce(ib)
			Expect(getImageBuild(ctx, ib)).To(haveCondition(meta.ReconcilingCondition, metav1.ConditionTrue, buildv1alpha1.BuildingReason))
			Expect(listJobs(ctx, namespace)).To(HaveLen(2))
		})
	})
})

var _ = Describe("BuildPodReconciler", func() {
	var (
		ctx       context.Context
		namespace string
		r         *BuildPodReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespace = newNamespace(ctx)
		r = &BuildPodReconciler{Client: k8sClient}
	})

	// startBuild creates an ImageBuild and the build Job a reconciliation creates for it.
	startBuild := func() (*buildv1alpha1.ImageBuild, *batchv1.Job) {
		ib := newImageBuild(ctx, namespace, nil)
		_, err := (&ImageBuildReconciler{Client: k8sClient, BuildConfig: testBuildConfig}).
			Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ib)})
		Expect(err).NotTo(HaveOccurred())
		return ib, onlyJob(ctx, namespace)
	}

	reconcilePod := func(pod *corev1.Pod) {
		GinkgoHelper()
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)})
		Expect(err).NotTo(HaveOccurred())
	}

	It("keeps a deleted pod until the ImageBuild records its result", func() {
		_, job := startBuild()
		pod := addPod(ctx, job)
		terminatePod(ctx, pod, 1, "boom")
		Expect(k8sClient.Delete(ctx, pod)).To(Succeed())

		reconcilePod(pod)

		Expect(hasFinalizer(ctx, pod)).To(BeTrue())
	})

	It("releases a deleted pod whose Job is gone", func() {
		_, job := startBuild()
		pod := addPod(ctx, job)
		Expect(k8sClient.Delete(ctx, job)).To(Succeed())
		Expect(k8sClient.Delete(ctx, pod)).To(Succeed())

		reconcilePod(pod)

		Expect(hasFinalizer(ctx, pod)).To(BeFalse())
	})

	It("releases a deleted pod whose ImageBuild is gone", func() {
		ib, job := startBuild()
		pod := addPod(ctx, job)
		Expect(k8sClient.Delete(ctx, ib)).To(Succeed())
		Expect(k8sClient.Delete(ctx, pod)).To(Succeed())

		reconcilePod(pod)

		Expect(hasFinalizer(ctx, pod)).To(BeFalse())
	})

	It("leaves pods that aren't being deleted alone", func() {
		_, job := startBuild()
		pod := addPod(ctx, job)
		Expect(k8sClient.Delete(ctx, job)).To(Succeed())

		reconcilePod(pod)

		Expect(hasFinalizer(ctx, pod)).To(BeTrue())
	})
})

var _ = Describe("ResolveOwnImage", func() {
	var namespace string

	BeforeEach(func() {
		namespace = newNamespace(context.Background())
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "controller"},
			Spec: corev1.PodSpec{Containers: []corev1.Container{
				{Name: "sidecar", Image: testPodImage},
				{Name: "manager", Image: "ghcr.io/leightweight/image-build-controller:v1.0.0"},
			}},
		}
		Expect(k8sClient.Create(context.Background(), pod)).To(Succeed())
	})

	It("returns the image of the named container in the controller's pod", func(ctx SpecContext) {
		Expect(ResolveOwnImage(ctx, k8sClient, namespace, "controller", "manager")).To(
			Equal("ghcr.io/leightweight/image-build-controller:v1.0.0"))
	})

	It("explains what's missing", func(ctx SpecContext) {
		_, err := ResolveOwnImage(ctx, k8sClient, "", "", "manager")
		Expect(err).To(MatchError(ContainSubstring("POD_NAMESPACE and POD_NAME")))
		_, err = ResolveOwnImage(ctx, k8sClient, namespace, "missing", "manager")
		Expect(err).To(MatchError(ContainSubstring("could not get the controller pod")))
		_, err = ResolveOwnImage(ctx, k8sClient, namespace, "controller", "other")
		Expect(err).To(MatchError(ContainSubstring(`no container named "other"`)))
	})
})

var _ = Describe("Build lifecycle with a running manager", Ordered, func() {
	var (
		cancelManager context.CancelFunc
		namespace     string
	)

	BeforeAll(func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:     scheme.Scheme,
			Metrics:    metricsserver.Options{BindAddress: "0"},
			Cache:      CacheOptions(),
			Controller: config.Controller{SkipNameValidation: new(true)},
		})
		Expect(err).NotTo(HaveOccurred())

		var mgrCtx context.Context
		mgrCtx, cancelManager = context.WithCancel(context.Background())
		Expect((&ImageBuildReconciler{
			Client:        mgr.GetClient(),
			EventRecorder: mgr.GetEventRecorder(ControllerName),
			BuildConfig:   testBuildConfig,
		}).SetupWithManager(mgrCtx, mgr)).To(Succeed())
		Expect((&BuildPodReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr)).To(Succeed())

		go func() {
			defer GinkgoRecover()
			Expect(mgr.Start(mgrCtx)).To(Succeed())
		}()
		namespace = newNamespace(context.Background())
	})

	AfterAll(func() {
		cancelManager()
	})

	It("records a finished build as soon as its Job and pod finish", func(ctx SpecContext) {
		ib := newImageBuild(ctx, namespace, nil, func(ib *buildv1alpha1.ImageBuild) { ib.Name = "watched" })
		var job *batchv1.Job
		Eventually(func() []batchv1.Job { return listJobs(ctx, namespace) }).WithContext(ctx).Should(HaveLen(1))
		job = onlyJob(ctx, namespace)

		// A cluster that deletes finished pods quickly deletes this one before the controller reads it.
		pod := addPod(ctx, job)
		terminatePod(ctx, pod, 0, testDigest)
		Expect(k8sClient.Delete(ctx, pod)).To(Succeed())
		setJobFinished(ctx, job, true, 1)

		// spec.interval is 10 minutes, so only the Job and pod watches can bring this in time.
		// The patch helper writes the conditions and the rest of the status in separate requests,
		// so wait for both.
		Eventually(func() *buildv1alpha1.ImageBuild { return getImageBuild(ctx, ib) }).WithContext(ctx).
			Should(And(
				haveCondition(meta.ReadyCondition, metav1.ConditionTrue, meta.SucceededReason),
				HaveField("Status.LatestImage", "registry.example/app@"+testDigest)))
		Eventually(func() bool { return hasFinalizer(ctx, pod) }).WithContext(ctx).Should(BeFalse(),
			"the pod is released, so its deletion completes")
	}, SpecTimeout(30*time.Second))

	It("releases the pods of a deleted ImageBuild", func(ctx SpecContext) {
		ib := newImageBuild(ctx, namespace, nil, func(ib *buildv1alpha1.ImageBuild) { ib.Name = "deleted" })
		var job *batchv1.Job
		Eventually(func() *batchv1.Job {
			for _, j := range listJobs(ctx, namespace) {
				if metav1.IsControlledBy(&j, getImageBuild(ctx, ib)) {
					return &j
				}
			}
			return nil
		}).WithContext(ctx).ShouldNot(BeNil())
		for _, j := range listJobs(ctx, namespace) {
			if metav1.IsControlledBy(&j, getImageBuild(ctx, ib)) {
				job = &j
			}
		}
		pod := addPod(ctx, job)

		// envtest has no garbage collector, so delete what it would delete.
		Expect(k8sClient.Delete(ctx, ib)).To(Succeed())
		Expect(k8sClient.Delete(ctx, pod)).To(Succeed())
		Expect(k8sClient.Delete(ctx, job)).To(Succeed())

		Eventually(func() bool { return hasFinalizer(ctx, pod) }).WithContext(ctx).Should(BeFalse())
	}, SpecTimeout(30*time.Second))
})

// timestamp formats a time the way the .Timestamp tag variable does.
func timestamp(t time.Time) string {
	return strconv.FormatInt(build.NewTagData(nil, 0, t).Timestamp, 10)
}

var _ = DescribeTable("truncateEventNote",
	func(message, want string) {
		Expect(truncateEventNote(message)).To(Equal(want))
	},
	Entry("short messages stay the same", "build failed", "build failed"),
	Entry("a message at the limit stays the same",
		strings.Repeat("a", maxEventNoteLength), strings.Repeat("a", maxEventNoteLength)),
	Entry("a longer message keeps its end",
		strings.Repeat("a", maxEventNoteLength)+"END", "..."+strings.Repeat("a", maxEventNoteLength-6)+"END"),
	Entry("the cut never splits a character",
		strings.Repeat("é", maxEventNoteLength), "..."+strings.Repeat("é", (maxEventNoteLength-4)/2)),
)

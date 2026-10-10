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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	aclapi "github.com/fluxcd/pkg/apis/acl"
	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/conditions"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	buildv1alpha1 "github.com/leightweight/image-build-controller/api/v1alpha1"
)

const defaultInterval = 10 * time.Minute

// newNamespace creates a namespace with a generated name, so each test works in isolation.
func newNamespace(ctx context.Context) string {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "imagebuild-test-"}}
	Expect(k8sClient.Create(ctx, ns)).To(Succeed())
	return ns.Name
}

// newGitRepository creates a GitRepository, and gives it an artifact when revision is set.
func newGitRepository(ctx context.Context, namespace, name, revision string) *sourcev1.GitRepository {
	repo := &sourcev1.GitRepository{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: sourcev1.GitRepositorySpec{
			URL:      "https://example.invalid/app.git",
			Interval: metav1.Duration{Duration: time.Minute},
		},
	}
	Expect(k8sClient.Create(ctx, repo)).To(Succeed())
	if revision != "" {
		setArtifact(ctx, repo, revision)
	}
	return repo
}

// setArtifact sets the GitRepository's status artifact the way source-controller does.
func setArtifact(ctx context.Context, repo *sourcev1.GitRepository, revision string) {
	repo.Status.Artifact = &meta.Artifact{
		Path:           "gitrepository/" + repo.Namespace + "/" + repo.Name + "/artifact.tar.gz",
		URL:            "http://source-controller.flux-system.svc.cluster.local./gitrepository/artifact.tar.gz",
		Revision:       revision,
		Digest:         "sha256:5d5d1b1c3c8e3c7f6f5e9e2b5f8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a291807",
		LastUpdateTime: metav1.Now(),
	}
	Expect(k8sClient.Status().Update(ctx, repo)).To(Succeed())
}

// newImageBuild creates an ImageBuild. With a nil sourceRef, it builds an inline Dockerfile.
func newImageBuild(ctx context.Context, namespace string, sourceRef *meta.NamespacedObjectKindReference,
	mutate ...func(*buildv1alpha1.ImageBuild)) *buildv1alpha1.ImageBuild {
	ib := &buildv1alpha1.ImageBuild{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "app"},
		Spec: buildv1alpha1.ImageBuildSpec{
			SourceRef: sourceRef,
			Push:      buildv1alpha1.Push{Image: "registry.example/app"},
		},
	}
	if sourceRef != nil {
		ib.Spec.Dockerfile.Path = "Dockerfile"
	} else {
		ib.Spec.Dockerfile.Inline = "FROM alpine\n"
	}
	for _, m := range mutate {
		m(ib)
	}
	Expect(k8sClient.Create(ctx, ib)).To(Succeed())
	return ib
}

func gitRepositoryRef(namespace, name string) *meta.NamespacedObjectKindReference {
	return &meta.NamespacedObjectKindReference{Kind: sourcev1.GitRepositoryKind, Namespace: namespace, Name: name}
}

func getImageBuild(ctx context.Context, ib *buildv1alpha1.ImageBuild) *buildv1alpha1.ImageBuild {
	got := &buildv1alpha1.ImageBuild{}
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ib), got)).To(Succeed())
	return got
}

// haveCondition matches an ImageBuild with a condition of the given type, status, and reason.
func haveCondition(conditionType string, status metav1.ConditionStatus, reason string) OmegaMatcher {
	return WithTransform(func(ib *buildv1alpha1.ImageBuild) *metav1.Condition {
		return conditions.Get(ib, conditionType)
	}, And(Not(BeNil()), HaveField("Status", status), HaveField("Reason", reason)))
}

// notHaveCondition matches an ImageBuild without a condition of the given type.
func notHaveCondition(conditionType string) OmegaMatcher {
	return WithTransform(func(ib *buildv1alpha1.ImageBuild) bool {
		return conditions.Has(ib, conditionType)
	}, BeFalse())
}

var _ = Describe("ImageBuildReconciler", func() {
	var (
		ctx        context.Context
		namespace  string
		recorder   *events.FakeRecorder
		reconciler *ImageBuildReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespace = newNamespace(ctx)
		recorder = events.NewFakeRecorder(10)
		reconciler = &ImageBuildReconciler{Client: k8sClient, EventRecorder: recorder}
	})

	reconcileOnce := func(ib *buildv1alpha1.ImageBuild) (ctrl.Result, error) {
		return reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ib)})
	}

	It("ignores an ImageBuild that doesn't exist", func() {
		result, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: namespace, Name: "missing"},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(BeZero())
	})

	Context("when suspended", func() {
		It("leaves the conditions alone but records a reconcile request", func() {
			ib := newImageBuild(ctx, namespace, nil, func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.Suspend = true
				ib.Annotations = map[string]string{meta.ReconcileRequestAnnotation: "token-1"}
			})

			result, err := reconcileOnce(ib)

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeZero())
			got := getImageBuild(ctx, ib)
			Expect(got.Status.Conditions).To(BeEmpty())
			Expect(got.Status.LastHandledReconcileAt).To(Equal("token-1"))
			Expect(recorder.Events).To(BeEmpty())
		})
	})

	Context("with an inline Dockerfile and no source", func() {
		It("skips source resolution and reports that builds aren't implemented yet", func() {
			ib := newImageBuild(ctx, namespace, nil)

			result, err := reconcileOnce(ib)

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeZero(), "a stalled ImageBuild waits for a change instead of requeueing")
			got := getImageBuild(ctx, ib)
			Expect(got).To(haveCondition(meta.ReadyCondition, metav1.ConditionFalse, BuildNotImplementedReason))
			Expect(got).To(haveCondition(meta.StalledCondition, metav1.ConditionTrue, BuildNotImplementedReason))
			Expect(got).To(notHaveCondition(meta.ReconcilingCondition))
			Expect(got.Status.ObservedGeneration).To(Equal(got.Generation))
		})
	})

	Context("with a GitRepository source", func() {
		It("waits when the GitRepository doesn't exist", func() {
			ib := newImageBuild(ctx, namespace, gitRepositoryRef("", "app"))

			result, err := reconcileOnce(ib)

			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(defaultInterval))
			got := getImageBuild(ctx, ib)
			Expect(got).To(haveCondition(meta.ReadyCondition, metav1.ConditionFalse, buildv1alpha1.SourceNotReadyReason))
			Expect(conditions.GetMessage(got, meta.ReadyCondition)).To(ContainSubstring(namespace + "/app was not found"))
			Expect(got).To(haveCondition(meta.ReconcilingCondition, metav1.ConditionTrue, meta.ProgressingWithRetryReason))
			Expect(got).To(notHaveCondition(meta.StalledCondition))
			Expect(got.Status.ObservedGeneration).To(BeZero(), "kstatus reports the ImageBuild as in progress")
		})

		It("waits when the GitRepository has no artifact yet", func() {
			newGitRepository(ctx, namespace, "app", "")
			ib := newImageBuild(ctx, namespace, gitRepositoryRef("", "app"))

			result, err := reconcileOnce(ib)

			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(defaultInterval))
			got := getImageBuild(ctx, ib)
			Expect(got).To(haveCondition(meta.ReadyCondition, metav1.ConditionFalse, buildv1alpha1.SourceNotReadyReason))
			Expect(conditions.GetMessage(got, meta.ReadyCondition)).To(ContainSubstring("has no artifact yet"))
		})

		It("uses spec.interval as the retry interval while waiting", func() {
			ib := newImageBuild(ctx, namespace, gitRepositoryRef("", "app"), func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.Interval = metav1.Duration{Duration: 3 * time.Minute}
			})

			result, err := reconcileOnce(ib)

			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(3 * time.Minute))
		})

		It("continues when the GitRepository has an artifact", func() {
			newGitRepository(ctx, namespace, "app", "main@sha1:abc123")
			ib := newImageBuild(ctx, namespace, gitRepositoryRef("", "app"))

			_, err := reconcileOnce(ib)

			Expect(err).NotTo(HaveOccurred())
			got := getImageBuild(ctx, ib)
			Expect(got).To(haveCondition(meta.StalledCondition, metav1.ConditionTrue, BuildNotImplementedReason))
		})

		It("builds from the last artifact even if the GitRepository's latest fetch failed", func() {
			repo := newGitRepository(ctx, namespace, "app", "main@sha1:abc123")
			conditions.MarkFalse(repo, meta.ReadyCondition, meta.FailedReason, "could not clone")
			Expect(k8sClient.Status().Update(ctx, repo)).To(Succeed())
			ib := newImageBuild(ctx, namespace, gitRepositoryRef("", "app"))

			_, err := reconcileOnce(ib)

			Expect(err).NotTo(HaveOccurred())
			got := getImageBuild(ctx, ib)
			Expect(got).To(haveCondition(meta.StalledCondition, metav1.ConditionTrue, BuildNotImplementedReason))
		})

		It("allows a GitRepository in another namespace by default", func() {
			sourceNamespace := newNamespace(ctx)
			newGitRepository(ctx, sourceNamespace, "app", "main@sha1:abc123")
			ib := newImageBuild(ctx, namespace, gitRepositoryRef(sourceNamespace, "app"))

			_, err := reconcileOnce(ib)

			Expect(err).NotTo(HaveOccurred())
			Expect(getImageBuild(ctx, ib)).To(haveCondition(meta.StalledCondition, metav1.ConditionTrue, BuildNotImplementedReason))
		})

		Context("with --no-cross-namespace-refs", func() {
			BeforeEach(func() {
				reconciler.NoCrossNamespaceRefs = true
			})

			It("denies a GitRepository in another namespace", func() {
				sourceNamespace := newNamespace(ctx)
				newGitRepository(ctx, sourceNamespace, "app", "main@sha1:abc123")
				ib := newImageBuild(ctx, namespace, gitRepositoryRef(sourceNamespace, "app"))

				result, err := reconcileOnce(ib)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(BeZero())
				got := getImageBuild(ctx, ib)
				Expect(got).To(haveCondition(meta.StalledCondition, metav1.ConditionTrue, aclapi.AccessDeniedReason))
				Expect(got).To(haveCondition(meta.ReadyCondition, metav1.ConditionFalse, aclapi.AccessDeniedReason))
				Expect(conditions.GetMessage(got, meta.ReadyCondition)).To(
					ContainSubstring("Cross-namespace references are not allowed"))
				Expect(got).To(notHaveCondition(meta.ReconcilingCondition))
			})

			It("allows a GitRepository named with the ImageBuild's own namespace", func() {
				newGitRepository(ctx, namespace, "app", "main@sha1:abc123")
				ib := newImageBuild(ctx, namespace, gitRepositoryRef(namespace, "app"))

				_, err := reconcileOnce(ib)

				Expect(err).NotTo(HaveOccurred())
				Expect(getImageBuild(ctx, ib)).To(haveCondition(meta.StalledCondition, metav1.ConditionTrue, BuildNotImplementedReason))
			})
		})
	})

	It("records the reconcile request it handled", func() {
		ib := newImageBuild(ctx, namespace, nil, func(ib *buildv1alpha1.ImageBuild) {
			ib.Annotations = map[string]string{meta.ReconcileRequestAnnotation: "token-1"}
		})

		_, err := reconcileOnce(ib)

		Expect(err).NotTo(HaveOccurred())
		Expect(getImageBuild(ctx, ib).Status.LastHandledReconcileAt).To(Equal("token-1"))
	})

	It("updates observedGeneration when the spec changes", func() {
		ib := newImageBuild(ctx, namespace, nil)
		_, err := reconcileOnce(ib)
		Expect(err).NotTo(HaveOccurred())
		Expect(getImageBuild(ctx, ib).Status.ObservedGeneration).To(BeEquivalentTo(1))

		got := getImageBuild(ctx, ib)
		got.Spec.Target = "runtime"
		Expect(k8sClient.Update(ctx, got)).To(Succeed())
		_, err = reconcileOnce(ib)
		Expect(err).NotTo(HaveOccurred())

		Expect(getImageBuild(ctx, ib).Status.ObservedGeneration).To(BeEquivalentTo(2))
	})

	It("records an Event when Ready changes, and not again while it stays the same", func() {
		ib := newImageBuild(ctx, namespace, nil)

		_, err := reconcileOnce(ib)
		Expect(err).NotTo(HaveOccurred())
		Expect(recorder.Events).To(Receive(Equal("Warning " + BuildNotImplementedReason + " Building images is not implemented yet")))

		_, err = reconcileOnce(ib)
		Expect(err).NotTo(HaveOccurred())
		Expect(recorder.Events).To(BeEmpty())
	})
})

var _ = Describe("ImageBuildReconciler with a running manager", Ordered, func() {
	var (
		cancelManager context.CancelFunc
		namespace     string
	)

	BeforeAll(func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:  scheme.Scheme,
			Metrics: metricsserver.Options{BindAddress: "0"},
		})
		Expect(err).NotTo(HaveOccurred())

		var mgrCtx context.Context
		mgrCtx, cancelManager = context.WithCancel(context.Background())
		Expect((&ImageBuildReconciler{
			Client:        mgr.GetClient(),
			EventRecorder: mgr.GetEventRecorder(ControllerName),
		}).SetupWithManager(mgrCtx, mgr)).To(Succeed())

		go func() {
			defer GinkgoRecover()
			Expect(mgr.Start(mgrCtx)).To(Succeed())
		}()
		namespace = newNamespace(context.Background())
	})

	AfterAll(func() {
		cancelManager()
	})

	It("reconciles an ImageBuild again when its GitRepository gets an artifact", func(ctx SpecContext) {
		repo := newGitRepository(ctx, namespace, "app", "")
		ib := newImageBuild(ctx, namespace, gitRepositoryRef("", "app"))

		Eventually(func() *buildv1alpha1.ImageBuild { return getImageBuild(ctx, ib) }).
			WithContext(ctx).
			Should(haveCondition(meta.ReadyCondition, metav1.ConditionFalse, buildv1alpha1.SourceNotReadyReason))

		setArtifact(ctx, repo, "main@sha1:abc123")

		// spec.interval is 10 minutes, so only the GitRepository watch can trigger this reconciliation in time.
		Eventually(func() *buildv1alpha1.ImageBuild { return getImageBuild(ctx, ib) }).
			WithContext(ctx).
			Should(haveCondition(meta.StalledCondition, metav1.ConditionTrue, BuildNotImplementedReason))
	}, SpecTimeout(30*time.Second))

	It("ignores GitRepository updates that don't change the artifact", func(ctx SpecContext) {
		repo := newGitRepository(ctx, namespace, "other", "")
		ib := newImageBuild(ctx, namespace, gitRepositoryRef("", "other"), func(ib *buildv1alpha1.ImageBuild) {
			ib.Name = "other"
		})
		Eventually(func() *buildv1alpha1.ImageBuild { return getImageBuild(ctx, ib) }).
			WithContext(ctx).
			Should(haveCondition(meta.ReadyCondition, metav1.ConditionFalse, buildv1alpha1.SourceNotReadyReason))
		before := conditions.Get(getImageBuild(ctx, ib), meta.ReadyCondition).LastTransitionTime

		// A status change without an artifact must not enqueue the ImageBuild.
		conditions.MarkFalse(repo, meta.ReadyCondition, meta.FailedReason, "could not clone")
		Expect(k8sClient.Status().Update(ctx, repo)).To(Succeed())

		Consistently(func() metav1.Time {
			return conditions.Get(getImageBuild(ctx, ib), meta.ReadyCondition).LastTransitionTime
		}).WithContext(ctx).WithTimeout(3 * time.Second).Should(Equal(before))
	}, SpecTimeout(30*time.Second))
})

var _ = Describe("SourceArtifactChangedPredicate", func() {
	repo := func(revision, digest string) *sourcev1.GitRepository {
		r := &sourcev1.GitRepository{}
		if revision != "" {
			r.Status.Artifact = &meta.Artifact{Revision: revision, Digest: digest}
		}
		return r
	}
	p := SourceArtifactChangedPredicate{}

	DescribeTable("Update",
		func(oldRepo, newRepo *sourcev1.GitRepository, want bool) {
			Expect(p.Update(eventUpdate(oldRepo, newRepo))).To(Equal(want))
		},
		Entry("artifact appears", repo("", ""), repo("main@sha1:a", "sha256:1"), true),
		Entry("artifact disappears", repo("main@sha1:a", "sha256:1"), repo("", ""), true),
		Entry("revision changes", repo("main@sha1:a", "sha256:1"), repo("main@sha1:b", "sha256:1"), true),
		Entry("digest changes", repo("main@sha1:a", "sha256:1"), repo("main@sha1:a", "sha256:2"), true),
		Entry("nothing changes", repo("main@sha1:a", "sha256:1"), repo("main@sha1:a", "sha256:1"), false),
		Entry("still no artifact", repo("", ""), repo("", ""), false),
	)

	It("passes a created source only when it has an artifact", func() {
		Expect(p.Create(eventCreate(repo("main@sha1:a", "sha256:1")))).To(BeTrue())
		Expect(p.Create(eventCreate(repo("", "")))).To(BeFalse())
	})

	It("passes deleted sources and ignores generic events", func() {
		Expect(p.Delete(eventDelete(repo("", "")))).To(BeTrue())
		Expect(p.Generic(eventGeneric(repo("", "")))).To(BeFalse())
	})
})

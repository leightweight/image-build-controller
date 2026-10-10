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

package v1alpha1_test

import (
	"os"
	"path/filepath"
	"time"

	"github.com/fluxcd/pkg/apis/meta"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	buildv1alpha1 "github.com/leightweight/image-build-controller/api/v1alpha1"
)

// inlineDockerfile is a minimal Dockerfile for tests that set dockerfile.inline.
const inlineDockerfile = "FROM alpine"

// newImageBuild returns a valid ImageBuild that builds Dockerfile from a GitRepository.
func newImageBuild() *buildv1alpha1.ImageBuild {
	return &buildv1alpha1.ImageBuild{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "test-",
			Namespace:    "default",
		},
		Spec: buildv1alpha1.ImageBuildSpec{
			SourceRef: &meta.NamespacedObjectKindReference{
				Kind:      "GitRepository",
				Name:      "foo",
				Namespace: "flux-system",
			},
			Dockerfile: buildv1alpha1.Dockerfile{Path: "Dockerfile"},
			Push:       buildv1alpha1.Push{Image: "registry.example/foo/bar"},
		},
	}
}

// createAndGet creates the ImageBuild and reads it back, so the result includes the defaults the API server applied.
func createAndGet(ib *buildv1alpha1.ImageBuild) *buildv1alpha1.ImageBuild {
	GinkgoHelper()
	Expect(k8sClient.Create(ctx, ib)).To(Succeed())
	DeferCleanup(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, ib))).To(Succeed())
	})
	got := &buildv1alpha1.ImageBuild{}
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ib), got)).To(Succeed())
	return got
}

var _ = Describe("ImageBuild API", func() {
	Context("defaults", func() {
		It("applies the documented defaults", func() {
			got := createAndGet(newImageBuild())

			Expect(got.Spec.Cache.Enabled).To(Equal(new(true)))
			Expect(got.Spec.ConcurrencyPolicy).To(Equal(buildv1alpha1.ReplaceConcurrency))
			Expect(got.Spec.BackoffLimit).To(Equal(ptr.To[int32](2)))
			Expect(got.Spec.SuccessfulBuildsHistoryLimit).To(Equal(ptr.To[int32](1)))
			Expect(got.Spec.FailedBuildsHistoryLimit).To(Equal(ptr.To[int32](3)))
			Expect(got.Spec.Interval.Duration).To(Equal(10 * time.Minute))
			Expect(got.Spec.RebuildInterval).To(BeNil())
			// The controller's --default-user-namespaces flag applies when the field is unset.
			Expect(got.Spec.UserNamespaces).To(BeNil())
			Expect(got.Spec.Suspend).To(BeFalse())
			Expect(got.Spec.Push.Insecure).To(BeFalse())
			// The controller applies the default tag, because it depends on whether the build has a source.
			Expect(got.Spec.Push.Tags).To(BeEmpty())
		})

		It("keeps explicit false and zero values instead of replacing them with defaults", func() {
			ib := newImageBuild()
			ib.Spec.Cache.Enabled = new(false)
			ib.Spec.UserNamespaces = new(false)
			ib.Spec.BackoffLimit = ptr.To[int32](0)
			ib.Spec.SuccessfulBuildsHistoryLimit = ptr.To[int32](0)
			ib.Spec.FailedBuildsHistoryLimit = ptr.To[int32](0)
			got := createAndGet(ib)

			Expect(got.Spec.Cache.Enabled).To(Equal(new(false)))
			Expect(got.Spec.UserNamespaces).To(Equal(new(false)))
			Expect(got.Spec.BackoffLimit).To(Equal(ptr.To[int32](0)))
			Expect(got.Spec.SuccessfulBuildsHistoryLimit).To(Equal(ptr.To[int32](0)))
			Expect(got.Spec.FailedBuildsHistoryLimit).To(Equal(ptr.To[int32](0)))
		})

		It("keeps every field of a fully specified ImageBuild", func() {
			ib := newImageBuild()
			ib.Spec.Context = "sub/dir"
			ib.Spec.BuildArgs = map[string]string{"KEY": "value"}
			ib.Spec.Target = "runtime"
			ib.Spec.Push.Tags = []string{"{{ .Branch }}-{{ .ShortSHA }}-{{ .Timestamp }}", "latest"}
			ib.Spec.Push.SecretRef = &meta.LocalObjectReference{Name: "regcreds"}
			ib.Spec.Push.Insecure = true
			ib.Spec.ConcurrencyPolicy = buildv1alpha1.WaitConcurrency
			ib.Spec.RebuildInterval = &metav1.Duration{Duration: 168 * time.Hour}
			ib.Spec.UserNamespaces = new(true)
			ib.Spec.NodeSelector = map[string]string{"kubernetes.io/arch": "amd64"}
			ib.Spec.Interval = metav1.Duration{Duration: time.Hour}
			ib.Spec.Suspend = true
			got := createAndGet(ib)

			Expect(got.Spec).To(Equal(func() buildv1alpha1.ImageBuildSpec {
				want := ib.Spec
				want.Cache.Enabled = new(true)
				want.BackoffLimit = ptr.To[int32](2)
				want.SuccessfulBuildsHistoryLimit = ptr.To[int32](1)
				want.FailedBuildsHistoryLimit = ptr.To[int32](3)
				return want
			}()))
		})
	})

	Context("samples", func() {
		It("accepts the sample in config/samples", func() {
			data, err := os.ReadFile(filepath.Join("..", "..", "config", "samples", "build_v1alpha1_imagebuild.yaml"))
			Expect(err).NotTo(HaveOccurred())
			u := &unstructured.Unstructured{}
			Expect(yaml.Unmarshal(data, &u.Object)).To(Succeed())
			u.SetNamespace("default")
			Expect(k8sClient.Create(ctx, u)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, u))).To(Succeed())
			})
		})
	})

	Context("valid objects", func() {
		DescribeTable("are accepted",
			func(mutate func(*buildv1alpha1.ImageBuild)) {
				ib := newImageBuild()
				mutate(ib)
				createAndGet(ib)
			},
			Entry("a Dockerfile path with a GitRepository source", func(*buildv1alpha1.ImageBuild) {}),
			Entry("an inline Dockerfile with a source", func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.Dockerfile = buildv1alpha1.Dockerfile{Inline: inlineDockerfile}
			}),
			Entry("an inline Dockerfile without a source", func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.SourceRef = nil
				ib.Spec.Dockerfile = buildv1alpha1.Dockerfile{Inline: inlineDockerfile}
			}),
			Entry("a source in the ImageBuild namespace", func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.SourceRef.Namespace = ""
			}),
			Entry("a nested Dockerfile path", func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.Dockerfile.Path = "docker/app/Dockerfile"
			}),
			Entry("a Dockerfile path with dots in a name", func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.Dockerfile.Path = "docker/Dockerfile..prod"
			}),
			Entry("a context with a leading ./", func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.Context = "./subdir"
			}),
			Entry("a registry with a port", func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.Push.Image = "registry.registry.svc:5000/foo/bar"
			}),
			Entry("a Docker Hub image without a registry", func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.Push.Image = "foo/bar"
			}),
			Entry("a compound duration", func(ib *buildv1alpha1.ImageBuild) {
				ib.Spec.RebuildInterval = &metav1.Duration{Duration: 90 * time.Minute}
			}),
		)
	})

	Context("invalid objects", func() {
		DescribeTable("are rejected with a message naming the problem",
			func(mutate func(*buildv1alpha1.ImageBuild), message string) {
				ib := newImageBuild()
				mutate(ib)
				err := k8sClient.Create(ctx, ib)
				if err == nil {
					DeferCleanup(func() { _ = k8sClient.Delete(ctx, ib) })
				}
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an Invalid error, got %v", err)
				Expect(err.Error()).To(ContainSubstring(message))
			},
			Entry("both a Dockerfile path and inline contents",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Dockerfile.Inline = inlineDockerfile },
				"exactly one of path or inline must be set"),
			Entry("neither a Dockerfile path nor inline contents",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Dockerfile = buildv1alpha1.Dockerfile{} },
				"exactly one of path or inline must be set"),
			Entry("a Dockerfile path without a source",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.SourceRef = nil },
				"sourceRef is required when dockerfile.path is set"),
			Entry("a source that isn't a GitRepository",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.SourceRef.Kind = "OCIRepository" },
				"sourceRef.kind must be GitRepository"),
			Entry("a source without a name",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.SourceRef.Name = "" },
				"sourceRef.name must not be empty"),
			Entry("a registry Secret without a name",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Push.SecretRef = &meta.LocalObjectReference{} },
				"secretRef.name must not be empty"),
			Entry("an absolute Dockerfile path",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Dockerfile.Path = "/etc/Dockerfile" },
				"path must be a relative path"),
			Entry("a Dockerfile path that leaves the source",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Dockerfile.Path = "../Dockerfile" },
				"path must not contain '..'"),
			Entry("a Dockerfile path that leaves the source from a subdirectory",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Dockerfile.Path = "docker/../../Dockerfile" },
				"path must not contain '..'"),
			Entry("an absolute context",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Context = "/srv" },
				"context must be a relative path"),
			Entry("a context that leaves the source",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Context = "sub/../.." },
				"context must not contain '..'"),
			Entry("an empty image name",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Push.Image = "" },
				"spec.push.image"),
			Entry("an image name with a tag",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Push.Image = "registry.example/foo/bar:latest" },
				"image must not include a tag; set tags instead"),
			Entry("an image name with a registry port and a tag",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Push.Image = "registry.example:5000/foo/bar:v1" },
				"image must not include a tag; set tags instead"),
			Entry("an image name with a digest",
				func(ib *buildv1alpha1.ImageBuild) {
					ib.Spec.Push.Image = "registry.example/foo/bar@sha256:0123456789abcdef"
				},
				"image must not include a digest"),
			Entry("an unknown concurrency policy",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.ConcurrencyPolicy = "Queue" },
				"spec.concurrencyPolicy"),
			Entry("a negative backoff limit",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.BackoffLimit = ptr.To[int32](-1) },
				"spec.backoffLimit"),
			Entry("a negative successful builds history limit",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.SuccessfulBuildsHistoryLimit = ptr.To[int32](-1) },
				"spec.successfulBuildsHistoryLimit"),
			Entry("a negative failed builds history limit",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.FailedBuildsHistoryLimit = ptr.To[int32](-1) },
				"spec.failedBuildsHistoryLimit"),
			Entry("a negative interval",
				func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Interval = metav1.Duration{Duration: -time.Minute} },
				"spec.interval"),
			Entry("a negative rebuild interval",
				func(ib *buildv1alpha1.ImageBuild) {
					ib.Spec.RebuildInterval = &metav1.Duration{Duration: -time.Hour}
				},
				"spec.rebuildInterval"),
		)

		It("rejects an interval that isn't a Go duration", func() {
			ib := newImageBuild()
			obj := map[string]any{
				"apiVersion": buildv1alpha1.SchemeGroupVersion.String(),
				"kind":       buildv1alpha1.ImageBuildKind,
				"metadata":   map[string]any{"generateName": "test-", "namespace": "default"},
				"spec": map[string]any{
					"sourceRef":  map[string]any{"kind": "GitRepository", "name": "foo"},
					"dockerfile": map[string]any{"path": "Dockerfile"},
					"push":       map[string]any{"image": ib.Spec.Push.Image},
					"interval":   "10 minutes",
				},
			}
			u := &unstructured.Unstructured{Object: obj}
			err := k8sClient.Create(ctx, u)
			if err == nil {
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, u) })
			}
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an Invalid error, got %v", err)
			Expect(err.Error()).To(ContainSubstring("spec.interval"))
		})
	})

	Context("status", func() {
		It("stores the build status through the status subresource", func() {
			ib := createAndGet(newImageBuild())
			started := metav1.NewTime(time.Now().Truncate(time.Second))
			ib.Status = buildv1alpha1.ImageBuildStatus{
				ReconcileRequestStatus: meta.ReconcileRequestStatus{LastHandledReconcileAt: "2026-10-10T00:00:00Z"},
				ObservedGeneration:     ib.Generation,
				Conditions: []metav1.Condition{{
					Type:               meta.ReadyCondition,
					Status:             metav1.ConditionTrue,
					Reason:             meta.SucceededReason,
					Message:            "Built and pushed image",
					LastTransitionTime: started,
				}},
				LastBuild: &buildv1alpha1.LastBuild{
					BuildKey:       "1a2b3c4d",
					SourceRevision: "main@sha1:abc1234",
					Image: &buildv1alpha1.BuiltImage{
						Tags:   []string{"main-abc1234-1760000000"},
						Digest: "sha256:0123",
					},
					StartedAt:  &started,
					FinishedAt: &started,
					JobName:    "foo-1a2b3c4d",
					Result:     buildv1alpha1.BuildSucceeded,
				},
				LatestImage: "registry.example/foo/bar@sha256:0123",
			}
			Expect(k8sClient.Status().Update(ctx, ib)).To(Succeed())

			got := &buildv1alpha1.ImageBuild{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ib), got)).To(Succeed())
			Expect(got.Status).To(Equal(ib.Status))
		})

		It("rejects an unknown build result", func() {
			ib := createAndGet(newImageBuild())
			ib.Status.LastBuild = &buildv1alpha1.LastBuild{BuildKey: "1a2b3c4d", Result: "Maybe"}
			err := k8sClient.Status().Update(ctx, ib)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an Invalid error, got %v", err)
			Expect(err.Error()).To(ContainSubstring("status.lastBuild.result"))
		})
	})
})

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

package build

import (
	"time"

	"github.com/fluxcd/pkg/apis/meta"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	buildv1alpha1 "github.com/leightweight/image-build-controller/api/v1alpha1"
)

var keyTime = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func keyImageBuild() *buildv1alpha1.ImageBuild {
	return &buildv1alpha1.ImageBuild{
		Name: testName, Namespace: "apps",
		Spec: buildv1alpha1.ImageBuildSpec{
			SourceRef:  &meta.NamespacedObjectKindReference{Kind: "GitRepository", Name: testName},
			Dockerfile: buildv1alpha1.Dockerfile{Path: dockerfilePath},
			Context:    "src",
			BuildArgs:  map[string]string{"A": "1", "B": "2"},
			Target:     "runtime",
			Push: buildv1alpha1.Push{
				Image:     "registry.example/app",
				Tags:      []string{latestTag},
				SecretRef: &meta.LocalObjectReference{Name: testSecret},
			},
		},
	}
}

func keySource() *Source {
	return &Source{URL: "http://source/app.tar.gz", Digest: "sha256:aaaa", Revision: "main@sha1:0123456789abcdef"}
}

var _ = Describe("Key", func() {
	base := func() string { return Key(keyImageBuild(), keySource(), keyTime) }

	It("is a 64-character hex string", func() {
		Expect(base()).To(MatchRegexp(`^[0-9a-f]{64}$`))
	})

	It("is the same every time for the same inputs", func() {
		Expect(base()).To(Equal(base()))
	})

	It("doesn't depend on build arg order", func() {
		ib := keyImageBuild()
		ib.Spec.BuildArgs = map[string]string{"B": "2", "A": "1"}
		Expect(Key(ib, keySource(), keyTime)).To(Equal(base()))
	})

	DescribeTable("changes when something that decides the image changes",
		func(change func(*buildv1alpha1.ImageBuild, *Source)) {
			ib, src := keyImageBuild(), keySource()
			change(ib, src)
			Expect(Key(ib, src, keyTime)).NotTo(Equal(base()))
		},
		Entry("source digest", func(_ *buildv1alpha1.ImageBuild, s *Source) { s.Digest = "sha256:bbbb" }),
		Entry("Dockerfile path", func(ib *buildv1alpha1.ImageBuild, _ *Source) { ib.Spec.Dockerfile.Path = "other/Dockerfile" }),
		Entry("inline Dockerfile", func(ib *buildv1alpha1.ImageBuild, _ *Source) {
			ib.Spec.Dockerfile = buildv1alpha1.Dockerfile{Inline: "FROM alpine"}
		}),
		Entry("context", func(ib *buildv1alpha1.ImageBuild, _ *Source) { ib.Spec.Context = otherValue }),
		Entry("build arg value", func(ib *buildv1alpha1.ImageBuild, _ *Source) { ib.Spec.BuildArgs["A"] = "changed" }),
		Entry("added build arg", func(ib *buildv1alpha1.ImageBuild, _ *Source) { ib.Spec.BuildArgs["C"] = "3" }),
		Entry("target", func(ib *buildv1alpha1.ImageBuild, _ *Source) { ib.Spec.Target = "debug" }),
		Entry("image", func(ib *buildv1alpha1.ImageBuild, _ *Source) { ib.Spec.Push.Image = "registry.example/other" }),
		Entry("tag templates", func(ib *buildv1alpha1.ImageBuild, _ *Source) { ib.Spec.Push.Tags = []string{"stable"} }),
		Entry("secret name", func(ib *buildv1alpha1.ImageBuild, _ *Source) { ib.Spec.Push.SecretRef.Name = otherValue }),
		Entry("removed secret", func(ib *buildv1alpha1.ImageBuild, _ *Source) { ib.Spec.Push.SecretRef = nil }),
		Entry("insecure", func(ib *buildv1alpha1.ImageBuild, _ *Source) { ib.Spec.Push.Insecure = true }),
		Entry("requestedAt annotation", func(ib *buildv1alpha1.ImageBuild, _ *Source) {
			ib.Annotations = map[string]string{meta.ReconcileRequestAnnotation: "2026-10-10T12:00:00Z"}
		}),
		Entry("rebuildInterval", func(ib *buildv1alpha1.ImageBuild, _ *Source) {
			ib.Spec.RebuildInterval = &metav1.Duration{Duration: time.Hour}
		}),
	)

	DescribeTable("stays the same when only build settings change",
		func(change func(*buildv1alpha1.ImageBuild, *Source)) {
			ib, src := keyImageBuild(), keySource()
			change(ib, src)
			Expect(Key(ib, src, keyTime)).To(Equal(base()))
		},
		Entry("source revision with the same digest", func(_ *buildv1alpha1.ImageBuild, s *Source) {
			s.Revision = "main@sha1:fedcba9876543210"
		}),
		Entry("source URL", func(_ *buildv1alpha1.ImageBuild, s *Source) { s.URL = "http://elsewhere/app.tar.gz" }),
		Entry("cache", func(ib *buildv1alpha1.ImageBuild, _ *Source) { ib.Spec.Cache.Enabled = new(false) }),
		Entry("userNamespaces", func(ib *buildv1alpha1.ImageBuild, _ *Source) { ib.Spec.UserNamespaces = new(false) }),
		Entry("resources", func(ib *buildv1alpha1.ImageBuild, _ *Source) {
			ib.Spec.Resources = &corev1.ResourceRequirements{Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			}}
		}),
		Entry("nodeSelector", func(ib *buildv1alpha1.ImageBuild, _ *Source) {
			ib.Spec.NodeSelector = map[string]string{"kubernetes.io/arch": "amd64"}
		}),
		Entry("tolerations", func(ib *buildv1alpha1.ImageBuild, _ *Source) {
			ib.Spec.Tolerations = []corev1.Toleration{{Key: "builds", Operator: corev1.TolerationOpExists}}
		}),
		Entry("backoffLimit", func(ib *buildv1alpha1.ImageBuild, _ *Source) { ib.Spec.BackoffLimit = ptr.To[int32](5) }),
		Entry("history limits", func(ib *buildv1alpha1.ImageBuild, _ *Source) {
			ib.Spec.SuccessfulBuildsHistoryLimit = ptr.To[int32](9)
			ib.Spec.FailedBuildsHistoryLimit = ptr.To[int32](9)
		}),
		Entry("concurrencyPolicy", func(ib *buildv1alpha1.ImageBuild, _ *Source) {
			ib.Spec.ConcurrencyPolicy = buildv1alpha1.WaitConcurrency
		}),
		Entry("interval", func(ib *buildv1alpha1.ImageBuild, _ *Source) {
			ib.Spec.Interval = metav1.Duration{Duration: time.Hour}
		}),
		Entry("suspend", func(ib *buildv1alpha1.ImageBuild, _ *Source) { ib.Spec.Suspend = true }),
		Entry("labels and other annotations", func(ib *buildv1alpha1.ImageBuild, _ *Source) {
			ib.Labels = map[string]string{"team": "a"}
			ib.Annotations = map[string]string{"note": "x"}
		}),
		Entry("generation", func(ib *buildv1alpha1.ImageBuild, _ *Source) { ib.Generation = 7 }),
		Entry("zero rebuildInterval", func(ib *buildv1alpha1.ImageBuild, _ *Source) {
			ib.Spec.RebuildInterval = &metav1.Duration{}
		}),
	)

	It("ignores the context without a source, because the context is always empty", func() {
		ib := keyImageBuild()
		ib.Spec.SourceRef = nil
		ib.Spec.Dockerfile = buildv1alpha1.Dockerfile{Inline: "FROM alpine"}
		withContext := Key(ib, nil, keyTime)
		ib.Spec.Context = otherValue
		Expect(Key(ib, nil, keyTime)).To(Equal(withContext))
	})

	Context("with rebuildInterval", func() {
		key := func(now time.Time) string {
			ib := keyImageBuild()
			ib.Spec.RebuildInterval = &metav1.Duration{Duration: 24 * time.Hour}
			return Key(ib, keySource(), now)
		}
		periodStart := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)

		It("stays the same within one period", func() {
			Expect(key(periodStart)).To(Equal(key(periodStart.Add(24*time.Hour - time.Nanosecond))))
		})

		It("changes when the next period starts", func() {
			Expect(key(periodStart.Add(24 * time.Hour))).NotTo(Equal(key(periodStart)))
		})
	})

	It("doesn't depend on the time without rebuildInterval", func() {
		Expect(Key(keyImageBuild(), keySource(), keyTime.Add(1000*time.Hour))).To(Equal(base()))
	})
})

var _ = Describe("KeyPrefix", func() {
	It("returns the first 10 characters", func() {
		Expect(KeyPrefix("0123456789abcdef")).To(Equal("0123456789"))
	})

	It("returns short keys unchanged", func() {
		Expect(KeyPrefix("abc")).To(Equal("abc"))
	})
})

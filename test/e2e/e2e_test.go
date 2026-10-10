//go:build e2e

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

package e2e

import (
	"fmt"
	"time"

	"github.com/fluxcd/pkg/apis/meta"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apimeta "k8s.io/apimachinery/pkg/api/meta"

	buildv1alpha1 "github.com/leightweight/image-build-controller/api/v1alpha1"
)

const (
	// testNamespace holds the GitRepository, the ImageBuilds, and their build pods.
	testNamespace = "e2e"
	// repoName names both the Gitea repository and the GitRepository.
	repoName = "app"

	// buildTimeout covers pulling the BuildKit image on the first build.
	buildTimeout = 5 * time.Minute
)

var _ = Describe("ImageBuild", Ordered, func() {
	// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

	// firstCommit is the commit that the first build of the from-source ImageBuild uses.
	var firstCommit string

	BeforeAll(func() {
		By("recreating the test namespace")
		_, err := kubectl("delete", "namespace", testNamespace, "--ignore-not-found", "--timeout=2m")
		Expect(err).NotTo(HaveOccurred())
		_, err = kubectl("create", "namespace", testNamespace)
		Expect(err).NotTo(HaveOccurred())

		// BuildKit needs Unconfined seccomp and AppArmor profiles, which only the privileged level allows.
		By("labeling the test namespace with the privileged Pod Security level")
		_, err = kubectl("label", "--overwrite", "namespace", testNamespace,
			"pod-security.kubernetes.io/enforce=privileged")
		Expect(err).NotTo(HaveOccurred())

		By("creating the Git repository")
		recreateGitRepository(repoName)
		firstCommit = commitFiles(repoName, "Add a Dockerfile", map[string]string{
			"Dockerfile":  "FROM scratch\nCOPY . /\n",
			"message.txt": "built by the image-build-controller end-to-end test\n",
		})

		By("creating the GitRepository")
		Expect(kubectlApply(fmt.Sprintf(`
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: %s
  namespace: %s
spec:
  interval: 1m
  url: %s
  ref:
    branch: main
`, repoName, testNamespace, gitRepositoryURL(repoName)))).To(Succeed())
		_, err = kubectl("wait", "gitrepository/"+repoName, "-n", testNamespace,
			"--for=condition=Ready", "--timeout=2m")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			dumpDiagnostics(testNamespace)
		}
	})

	It("builds a Dockerfile from a GitRepository and pushes it", func() {
		image := registryHost + "/e2e/from-source"
		Expect(kubectlApply(fmt.Sprintf(`
apiVersion: build.fluxcd.leightweight.io/v1alpha1
kind: ImageBuild
metadata:
  name: from-source
  namespace: %s
spec:
  sourceRef:
    kind: GitRepository
    name: %s
  dockerfile:
    path: Dockerfile
  push:
    image: %s
    insecure: true
`, testNamespace, repoName, image))).To(Succeed())

		ib := waitForSuccessfulBuild(testNamespace, "from-source", firstCommit)
		expectDefaultBranchTag(ib, firstCommit)
		expectPushed(ib, image)
	})

	It("rebuilds when the GitRepository has a new commit", func() {
		image := registryHost + "/e2e/from-source"
		secondCommit := commitFiles(repoName, "Add a second file", map[string]string{
			"second.txt": "added by the second commit\n",
		})

		By("asking source-controller to fetch the new commit")
		_, err := kubectl("annotate", "--overwrite", "gitrepository/"+repoName, "-n", testNamespace,
			fmt.Sprintf("%s=%d", meta.ReconcileRequestAnnotation, time.Now().UnixNano()))
		Expect(err).NotTo(HaveOccurred())

		ib := waitForSuccessfulBuild(testNamespace, "from-source", secondCommit)
		expectDefaultBranchTag(ib, secondCommit)
		expectPushed(ib, image)
	})

	It("builds an inline Dockerfile without a source", func() {
		image := registryHost + "/e2e/inline"
		Expect(kubectlApply(fmt.Sprintf(`
apiVersion: build.fluxcd.leightweight.io/v1alpha1
kind: ImageBuild
metadata:
  name: inline
  namespace: %s
spec:
  dockerfile:
    inline: |
      FROM scratch
      COPY <<EOF /message.txt
      built from an inline Dockerfile
      EOF
  push:
    image: %s
    insecure: true
`, testNamespace, image))).To(Succeed())

		ib := waitForSuccessfulBuild(testNamespace, "inline", "")
		Expect(ib.Status.LastBuild.Image.Tags).To(ConsistOf(MatchRegexp(`^build-[0-9]+$`)))
		expectPushed(ib, image)
	})

	It("reports a failed build with the end of the build log", func() {
		// The build context is empty without a source, so the COPY fails.
		Expect(kubectlApply(fmt.Sprintf(`
apiVersion: build.fluxcd.leightweight.io/v1alpha1
kind: ImageBuild
metadata:
  name: failing
  namespace: %s
spec:
  dockerfile:
    inline: |
      FROM scratch
      COPY e2e-missing-file /
  push:
    image: %s/e2e/failing
    insecure: true
  backoffLimit: 0
`, testNamespace, registryHost))).To(Succeed())

		Eventually(func(g Gomega) {
			ib := getImageBuild(g, testNamespace, "failing")
			ready := apimeta.FindStatusCondition(ib.Status.Conditions, meta.ReadyCondition)
			g.Expect(ready).NotTo(BeNil())
			g.Expect(ready.Reason).To(Equal(buildv1alpha1.BuildFailedReason))
			g.Expect(ready.Message).To(ContainSubstring("e2e-missing-file"))
			g.Expect(apimeta.IsStatusConditionTrue(ib.Status.Conditions, meta.StalledCondition)).To(BeTrue())
			g.Expect(ib.Status.LastBuild).NotTo(BeNil())
			g.Expect(ib.Status.LastBuild.Result).To(Equal(buildv1alpha1.BuildFailed))
		}, buildTimeout, time.Second).Should(Succeed())
	})

	// +kubebuilder:scaffold:e2e-webhooks-checks
})

// waitForSuccessfulBuild waits until an ImageBuild is Ready after building the given commit.
// It stops early if the build fails.
// An empty commit matches a build without a source.
func waitForSuccessfulBuild(namespace, name, commit string) *buildv1alpha1.ImageBuild {
	var ib *buildv1alpha1.ImageBuild
	EventuallyWithOffset(1, func(g Gomega) {
		ib = getImageBuild(g, namespace, name)
		last := ib.Status.LastBuild
		if last != nil && last.Result == buildv1alpha1.BuildFailed && hasCommit(last.SourceRevision, commit) {
			ready := apimeta.FindStatusCondition(ib.Status.Conditions, meta.ReadyCondition)
			StopTrying(fmt.Sprintf("the build failed: %v", ready)).Now()
		}
		g.Expect(last).NotTo(BeNil())
		g.Expect(last.Result).To(Equal(buildv1alpha1.BuildSucceeded))
		g.Expect(hasCommit(last.SourceRevision, commit)).To(BeTrue(),
			"last build revision %q is not commit %q", last.SourceRevision, commit)
		g.Expect(apimeta.IsStatusConditionTrue(ib.Status.Conditions, meta.ReadyCondition)).To(BeTrue())
	}, buildTimeout, time.Second).Should(Succeed())
	return ib
}

// hasCommit reports whether a Flux revision such as main@sha1:<sha> is the given commit.
func hasCommit(revision, commit string) bool {
	if commit == "" {
		return revision == ""
	}
	return revision == "main@sha1:"+commit
}

// expectDefaultBranchTag checks the default tag for a build of the main branch.
func expectDefaultBranchTag(ib *buildv1alpha1.ImageBuild, commit string) {
	ExpectWithOffset(1, ib.Status.LastBuild.Image.Tags).To(ConsistOf(
		MatchRegexp(`^main-%s-[0-9]+$`, commit[:7])))
}

// expectPushed checks that the registry serves the build's digest for every tag it pushed.
func expectPushed(ib *buildv1alpha1.ImageBuild, image string) {
	built := ib.Status.LastBuild.Image
	ExpectWithOffset(1, built.Digest).To(HavePrefix("sha256:"))
	ExpectWithOffset(1, ib.Status.LatestImage).To(Equal(image + "@" + built.Digest))
	for _, tag := range built.Tags {
		digest, err := registryDigest(ib.Namespace, image+":"+tag)
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		ExpectWithOffset(1, digest).To(Equal(built.Digest), "registry digest for tag %s", tag)
	}
}

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
	"os"
	"os/exec"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/leightweight/image-build-controller/test/utils"
)

// managerImage is the manager image to be built and loaded for testing.
// Build pods also use it for the fetch step.
var managerImage = "example.com/image-build-controller:v0.0.1"

// TestE2E builds real images on the kind cluster that `mise run test:e2e` creates.
//
// To enable kubectl kuberc (use custom kubectl configurations), set: KUBECTL_KUBERC=true
// By default, kuberc is disabled to ensure consistent test behavior across different environments.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting image-build-controller e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

// BeforeSuite installs everything the builds need.
// Every step is idempotent, so the suite can rerun on a cluster kept with --keep-cluster.
var _ = BeforeSuite(func() {
	By("building the manager image")
	cmd := exec.Command("mise", "run", "docker-build", managerImage)
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the manager image")

	By("loading the manager image on Kind")
	err = utils.LoadImageToKindClusterWithName(managerImage)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the manager image into Kind")

	configureKubectlKubeRC()

	By("installing Flux source-controller")
	cmd = exec.Command("flux", "install", "--components=source-controller")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to install Flux")

	By("installing the Git server, the registry, and the source-controller NetworkPolicy")
	cmd = exec.Command("kubectl", "apply",
		"-f", "test/e2e/testdata/gitea.yaml",
		"-f", "test/e2e/testdata/registry.yaml",
		"-f", "test/e2e/testdata/source-controller-networkpolicy.yaml",
	)
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to install the test services")

	By("deploying the controller")
	cmd = exec.Command("mise", "run", "deploy", managerImage)
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to deploy the controller")

	for _, deployment := range []struct{ namespace, name string }{
		{giteaNamespace, giteaDeployment},
		{"registry", "registry"},
		{controllerNamespace, controllerDeployment},
	} {
		By(fmt.Sprintf("waiting for deployment %s/%s", deployment.namespace, deployment.name))
		cmd = exec.Command("kubectl", "rollout", "status", "deployment/"+deployment.name,
			"-n", deployment.namespace, "--timeout=5m")
		_, err = utils.Run(cmd)
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
	}

	By("creating the Gitea user")
	createGiteaUser()
})

// Disable kubectl kuberc by default for test isolation.
// This prevents local kubectl configurations from affecting test behavior.
// To enable kuberc, set: KUBECTL_KUBERC=true
func configureKubectlKubeRC() {
	if os.Getenv("KUBECTL_KUBERC") != "true" {
		By("disabling kubectl kuberc for test isolation")
		err := os.Setenv("KUBECTL_KUBERC", "false")
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to disable kubectl kuberc")
		_, _ = fmt.Fprintf(GinkgoWriter,
			"kubectl kuberc disabled for consistent test behavior (override with KUBECTL_KUBERC=true)\n")
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "kubectl kuberc enabled (KUBECTL_KUBERC=true)\n")
	}
}

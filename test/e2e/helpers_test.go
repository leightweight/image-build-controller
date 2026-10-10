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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	buildv1alpha1 "github.com/leightweight/image-build-controller/api/v1alpha1"
	"github.com/leightweight/image-build-controller/test/utils"
)

const (
	controllerNamespace  = "image-build-controller-system"
	controllerDeployment = "image-build-controller-controller-manager"

	giteaNamespace  = "gitea"
	giteaDeployment = "gitea"
	giteaUser       = "e2e"
	giteaPassword   = "e2e-password"

	// registryHost is the in-cluster registry that builds push to.
	registryHost = "registry.registry.svc.cluster.local:5000"
	craneImage   = "gcr.io/go-containerregistry/crane:v0.22.1"
)

// kubectl runs kubectl with the given arguments and returns its combined output.
func kubectl(args ...string) (string, error) {
	return utils.Run(exec.Command("kubectl", args...))
}

// kubectlApply applies a manifest from a string.
func kubectlApply(manifest string) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	return err
}

// giteaAPI calls the Gitea API from inside the Gitea pod, so the test needs no port forward.
func giteaAPI(method, path string, body any) (string, error) {
	args := []string{
		"exec", "-n", giteaNamespace, "deploy/" + giteaDeployment, "--",
		"curl", "--silent", "--show-error", "--fail-with-body",
		"--user", giteaUser + ":" + giteaPassword,
		"--request", method,
		"http://localhost:3000/api/v1" + path,
	}
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		args = append(args, "--header", "Content-Type: application/json", "--data", string(data))
	}
	return kubectl(args...)
}

// createGiteaUser creates the admin user that owns the test repositories.
func createGiteaUser() {
	out, err := kubectl("exec", "-n", giteaNamespace, "deploy/"+giteaDeployment, "--",
		"gitea", "admin", "user", "create",
		"--username", giteaUser,
		"--password", giteaPassword,
		"--email", giteaUser+"@example.com",
		"--admin",
		"--must-change-password=false",
	)
	if err != nil && !strings.Contains(out, "already exists") {
		Fail(fmt.Sprintf("Failed to create the Gitea user: %v", err))
	}
}

// recreateGitRepository deletes the Gitea repository if it exists and creates it with one commit on main.
func recreateGitRepository(name string) {
	_, _ = giteaAPI("DELETE", "/repos/"+giteaUser+"/"+name, nil)
	_, err := giteaAPI("POST", "/user/repos", map[string]any{
		"name":           name,
		"auto_init":      true,
		"readme":         "Default",
		"default_branch": "main",
	})
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to create the Gitea repository")
}

// gitRepositoryURL is the clone URL that source-controller uses.
func gitRepositoryURL(name string) string {
	return fmt.Sprintf("http://gitea.gitea.svc.cluster.local:3000/%s/%s.git", giteaUser, name)
}

// commitFiles adds files to the main branch of a Gitea repository in one commit and returns the commit SHA.
func commitFiles(repo, message string, files map[string]string) string {
	changes := make([]map[string]string, 0, len(files))
	for path, content := range files {
		changes = append(changes, map[string]string{
			"operation": "create",
			"path":      path,
			"content":   base64.StdEncoding.EncodeToString([]byte(content)),
		})
	}
	out, err := giteaAPI("POST", "/repos/"+giteaUser+"/"+repo+"/contents", map[string]any{
		"branch":  "main",
		"message": message,
		"files":   changes,
	})
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to commit to the Gitea repository")

	var response struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	ExpectWithOffset(1, json.Unmarshal([]byte(out), &response)).To(Succeed())
	ExpectWithOffset(1, response.Commit.SHA).NotTo(BeEmpty())
	return response.Commit.SHA
}

// getImageBuild reads an ImageBuild through kubectl.
func getImageBuild(g Gomega, namespace, name string) *buildv1alpha1.ImageBuild {
	out, err := kubectl("get", "imagebuild", name, "-n", namespace, "-o", "json")
	g.ExpectWithOffset(1, err).NotTo(HaveOccurred())
	ib := &buildv1alpha1.ImageBuild{}
	g.ExpectWithOffset(1, json.Unmarshal([]byte(out), ib)).To(Succeed())
	return ib
}

// registryDigest returns the digest that the registry serves for an image reference.
// It runs crane in a pod, because the registry is only reachable inside the cluster.
func registryDigest(namespace, ref string) (string, error) {
	pod := fmt.Sprintf("crane-%d", time.Now().UnixNano())
	defer func() {
		_, _ = kubectl("delete", "pod", pod, "-n", namespace, "--ignore-not-found", "--wait=false")
	}()

	if _, err := kubectl("run", pod, "-n", namespace, "--restart=Never", "--image="+craneImage,
		"--", "digest", "--insecure", ref); err != nil {
		return "", err
	}
	if _, err := kubectl("wait", "pod/"+pod, "-n", namespace, "--timeout=2m",
		"--for=jsonpath={.status.phase}=Succeeded"); err != nil {
		logs, _ := kubectl("logs", pod, "-n", namespace)
		return "", fmt.Errorf("crane failed: %w: %s", err, logs)
	}
	out, err := kubectl("logs", pod, "-n", namespace)
	return strings.TrimSpace(out), err
}

// dumpDiagnostics writes the state of the test namespace and the controller logs to the Ginkgo output.
func dumpDiagnostics(namespace string) {
	for _, args := range [][]string{
		{"logs", "deploy/" + controllerDeployment, "-n", controllerNamespace, "--tail=-1"},
		{"get", "gitrepository,imagebuild,job,pod", "-n", namespace, "-o", "yaml"},
		{"get", "events", "-n", namespace, "--sort-by=.lastTimestamp"},
		{"logs", "-n", namespace, "-l", "app.kubernetes.io/managed-by=image-build-controller",
			"--all-containers", "--prefix", "--tail=-1"},
	} {
		out, err := kubectl(args...)
		if err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "kubectl %s failed: %v\n", strings.Join(args, " "), err)
			continue
		}
		_, _ = fmt.Fprintf(GinkgoWriter, "kubectl %s:\n%s\n", strings.Join(args, " "), out)
	}
}

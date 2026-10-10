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
	"context"
	"strings"
	"time"

	"github.com/fluxcd/pkg/apis/meta"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	buildv1alpha1 "github.com/leightweight/image-build-controller/api/v1alpha1"
)

const (
	testImage      = "registry.example/team/app"
	testKey        = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testName       = "app"
	testSecret     = "regcreds"
	dockerfilePath = "Dockerfile"
	latestTag      = "latest"
	otherValue     = "other"
	mainBranch     = "main"
	contextArg     = "context=/workspace/context"
)

var (
	jobTime   = time.Unix(1760000000, 0)
	jobConfig = Config{
		BuildKitImage:         DefaultBuildKitImage,
		FetchImage:            "ghcr.io/leightweight/image-build-controller:v0.1.0",
		DefaultUserNamespaces: true,
	}
)

func sourceImageBuild() *buildv1alpha1.ImageBuild {
	return &buildv1alpha1.ImageBuild{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: "default", UID: "uid-1", Generation: 2},
		Spec: buildv1alpha1.ImageBuildSpec{
			SourceRef:    &meta.NamespacedObjectKindReference{Kind: "GitRepository", Name: testName},
			Dockerfile:   buildv1alpha1.Dockerfile{Path: dockerfilePath},
			Push:         buildv1alpha1.Push{Image: testImage, SecretRef: &meta.LocalObjectReference{Name: testSecret}},
			Cache:        buildv1alpha1.Cache{Enabled: new(true)},
			BackoffLimit: ptr.To[int32](2),
		},
	}
}

func inlineImageBuild() *buildv1alpha1.ImageBuild {
	ib := sourceImageBuild()
	ib.Spec.SourceRef = nil
	ib.Spec.Dockerfile = buildv1alpha1.Dockerfile{Inline: "FROM alpine:3.22\nRUN apk add curl\n"}
	return ib
}

func testSource() *Source {
	return &Source{
		URL:      "http://source-controller.flux-system.svc.cluster.local./gitrepository/default/app/abc.tar.gz",
		Digest:   "sha256:abc",
		Revision: "main@sha1:" + sha1Hash,
	}
}

func renderJob(ib *buildv1alpha1.ImageBuild, src *Source, cfg Config) *batchv1.Job {
	job, err := Job(ib, src, cfg, testKey, jobTime)
	Expect(err).NotTo(HaveOccurred())
	return job
}

// expectValid creates the Job on the envtest API server as a dry run, so the server validates the whole spec.
func expectValid(job *batchv1.Job) {
	GinkgoHelper()
	Expect(k8sClient.Create(context.Background(), job.DeepCopy(), client.DryRunAll)).To(Succeed())
}

func container(job *batchv1.Job, name string) *corev1.Container {
	GinkgoHelper()
	spec := &job.Spec.Template.Spec
	for i := range spec.InitContainers {
		if spec.InitContainers[i].Name == name {
			return &spec.InitContainers[i]
		}
	}
	for i := range spec.Containers {
		if spec.Containers[i].Name == name {
			return &spec.Containers[i]
		}
	}
	Fail("no container named " + name)
	return nil
}

func volumeNames(job *batchv1.Job) []string {
	names := make([]string, 0, len(job.Spec.Template.Spec.Volumes))
	for _, v := range job.Spec.Template.Spec.Volumes {
		names = append(names, v.Name)
	}
	return names
}

func mountPaths(c *corev1.Container) []string {
	paths := make([]string, 0, len(c.VolumeMounts))
	for _, m := range c.VolumeMounts {
		paths = append(paths, m.MountPath)
	}
	return paths
}

func envValue(c *corev1.Container, name string) string {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

func hasEnv(c *corev1.Container, name string) bool {
	for _, e := range c.Env {
		if e.Name == name {
			return true
		}
	}
	return false
}

// argValues returns the values that follow each occurrence of flag in args.
func argValues(args []string, flag string) []string {
	var values []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			values = append(values, args[i+1])
		}
	}
	return values
}

var _ = Describe("Job", func() {
	It("uses the group from the API package for its annotations and finalizer", func() {
		Expect(group).To(Equal(buildv1alpha1.GroupVersion.Group))
	})

	Context("with a source and user namespaces", func() {
		var job *batchv1.Job
		BeforeEach(func() { job = renderJob(sourceImageBuild(), testSource(), jobConfig) })

		It("is valid", func() { expectValid(job) })

		It("names, labels, and owns the Job", func() {
			Expect(job.Name).To(Equal("app-0123456789"))
			Expect(job.Namespace).To(Equal("default"))
			Expect(job.Labels).To(HaveKeyWithValue(ManagedByLabel, ManagedByValue))
			Expect(job.Spec.Template.Labels).To(HaveKeyWithValue(ManagedByLabel, ManagedByValue))
			Expect(job.Annotations).To(Equal(map[string]string{
				BuildKeyAnnotation:       testKey,
				SourceRevisionAnnotation: "main@sha1:" + sha1Hash,
				TagsAnnotation:           "main-0123456-1760000000",
				ImageAnnotation:          "registry.example/team/app",
			}))
			Expect(job.OwnerReferences).To(ConsistOf(metav1.OwnerReference{
				APIVersion:         "build.fluxcd.leightweight.io/v1alpha1",
				Kind:               "ImageBuild",
				Name:               testName,
				UID:                "uid-1",
				Controller:         new(true),
				BlockOwnerDeletion: new(true),
			}))
		})

		It("runs one pod that isn't restarted and holds the termination message finalizer", func() {
			Expect(job.Spec.BackoffLimit).To(Equal(ptr.To[int32](2)))
			pod := job.Spec.Template
			Expect(pod.Finalizers).To(ConsistOf(TerminationMessageFinalizer))
			Expect(pod.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyNever))
			Expect(pod.Spec.AutomountServiceAccountToken).To(Equal(new(false)))
		})

		It("runs as the build user in a user namespace", func() {
			spec := job.Spec.Template.Spec
			Expect(spec.HostUsers).To(Equal(new(false)))
			Expect(spec.SecurityContext.RunAsUser).To(Equal(ptr.To[int64](1000)))
			Expect(spec.SecurityContext.RunAsGroup).To(Equal(ptr.To[int64](1000)))
			Expect(spec.SecurityContext.RunAsNonRoot).To(Equal(new(true)))
		})

		It("fetches the source into the context directory with a locked-down init container", func() {
			fetch := container(job, FetchContainerName)
			Expect(fetch.Image).To(Equal(jobConfig.FetchImage))
			Expect(fetch.Args).To(Equal([]string{
				"fetch", "--url", testSource().URL, "--digest", "sha256:abc", "--dest", "/workspace/context",
			}))
			Expect(fetch.SecurityContext.AllowPrivilegeEscalation).To(Equal(new(false)))
			Expect(fetch.SecurityContext.ReadOnlyRootFilesystem).To(Equal(new(true)))
			Expect(fetch.SecurityContext.Capabilities.Drop).To(ConsistOf(corev1.Capability("ALL")))
			Expect(fetch.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))
			Expect(envValue(fetch, "TMPDIR")).To(Equal("/tmp"))
			Expect(mountPaths(fetch)).To(ConsistOf("/workspace/context", "/tmp"))
			Expect(fetch.TerminationMessagePolicy).To(Equal(corev1.TerminationMessageFallbackToLogsOnError))
		})

		It("unmasks /proc and keeps the process sandbox", func() {
			build := container(job, BuildContainerName)
			Expect(build.SecurityContext.ProcMount).To(Equal(new(corev1.UnmaskedProcMount)))
			Expect(build.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeUnconfined))
			Expect(build.SecurityContext.AppArmorProfile.Type).To(Equal(corev1.AppArmorProfileTypeUnconfined))
			Expect(hasEnv(build, "BUILDKITD_FLAGS")).To(BeFalse())
		})

		It("mounts subuid and subgid files from the downward API", func() {
			Expect(job.Spec.Template.Annotations).To(HaveKeyWithValue(subIDsAnnotation, "user:1001:64535\n"))
			build := container(job, BuildContainerName)
			Expect(build.VolumeMounts).To(ContainElements(
				corev1.VolumeMount{Name: subIDsVolume, MountPath: "/etc/subuid", SubPath: "subuid", ReadOnly: true},
				corev1.VolumeMount{Name: subIDsVolume, MountPath: "/etc/subgid", SubPath: "subgid", ReadOnly: true},
			))
			var subIDs *corev1.Volume
			for i, v := range job.Spec.Template.Spec.Volumes {
				if v.Name == subIDsVolume {
					subIDs = &job.Spec.Template.Spec.Volumes[i]
				}
			}
			Expect(subIDs).NotTo(BeNil())
			Expect(subIDs.DownwardAPI.Items).To(ConsistOf(
				corev1.DownwardAPIVolumeFile{Path: "subuid", FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.annotations['build.fluxcd.leightweight.io/subids']",
				}},
				corev1.DownwardAPIVolumeFile{Path: "subgid", FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.annotations['build.fluxcd.leightweight.io/subids']",
				}},
			))
		})

		It("runs buildctl through the script that reports the digest", func() {
			build := container(job, BuildContainerName)
			Expect(build.Image).To(Equal(DefaultBuildKitImage))
			Expect(build.Command).To(Equal([]string{"sh", "-c", buildScript, "build"}))
			Expect(buildScript).To(ContainSubstring(`buildctl-daemonless.sh "$@" --metadata-file /workspace/output/metadata.json`))
			Expect(buildScript).To(ContainSubstring("> /dev/termination-log"))
			Expect(build.TerminationMessagePolicy).To(Equal(corev1.TerminationMessageFallbackToLogsOnError))
		})

		It("builds the Dockerfile from the source and pushes every tag with the cache", func() {
			Expect(container(job, BuildContainerName).Args).To(Equal([]string{
				"build",
				"--frontend", "dockerfile.v0",
				"--local", contextArg,
				"--local", "dockerfile=/workspace/context",
				"--opt", "filename=Dockerfile",
				"--output", `type=image,"name=` + testImage + `:main-0123456-1760000000",push=true`,
				"--export-cache", "type=registry,ref=" + testImage + ":buildcache,mode=max",
				"--import-cache", "type=registry,ref=" + testImage + ":buildcache",
			}))
		})

		It("mounts the registry credentials as a Docker config", func() {
			build := container(job, BuildContainerName)
			Expect(envValue(build, "DOCKER_CONFIG")).To(Equal("/workspace/docker-config"))
			Expect(build.VolumeMounts).To(ContainElement(corev1.VolumeMount{
				Name: "docker-config", MountPath: "/workspace/docker-config", ReadOnly: true,
			}))
			Expect(job.Spec.Template.Spec.Volumes).To(ContainElement(corev1.Volume{
				Name: "docker-config",
				VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
					SecretName: testSecret,
					Items:      []corev1.KeyToPath{{Key: ".dockerconfigjson", Path: "config.json"}},
				}},
			}))
		})

		It("gives BuildKit writable state, output, and temporary directories", func() {
			Expect(mountPaths(container(job, BuildContainerName))).To(ContainElements(
				"/home/user/.local/share/buildkit", "/workspace/output", "/tmp",
			))
		})
	})

	Context("with a source and without user namespaces", func() {
		var job *batchv1.Job
		BeforeEach(func() {
			ib := sourceImageBuild()
			ib.Spec.UserNamespaces = new(false)
			job = renderJob(ib, testSource(), jobConfig)
		})

		It("is valid", func() { expectValid(job) })

		It("shares the daemon's process namespace instead of unmasking /proc", func() {
			Expect(job.Spec.Template.Spec.HostUsers).To(BeNil())
			build := container(job, BuildContainerName)
			Expect(build.SecurityContext.ProcMount).To(BeNil())
			Expect(envValue(build, "BUILDKITD_FLAGS")).To(Equal("--oci-worker-no-process-sandbox"))
			Expect(build.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeUnconfined))
			Expect(build.SecurityContext.AppArmorProfile.Type).To(Equal(corev1.AppArmorProfileTypeUnconfined))
		})

		It("doesn't mount subuid and subgid files", func() {
			Expect(job.Spec.Template.Annotations).NotTo(HaveKey(subIDsAnnotation))
			Expect(volumeNames(job)).NotTo(ContainElement(subIDsVolume))
			Expect(mountPaths(container(job, BuildContainerName))).NotTo(ContainElements("/etc/subuid", "/etc/subgid"))
		})
	})

	Context("with userNamespaces unset", func() {
		It("follows the controller default", func() {
			ib := sourceImageBuild()
			Expect(renderJob(ib, testSource(), jobConfig).Spec.Template.Spec.HostUsers).To(Equal(new(false)))
			cfg := jobConfig
			cfg.DefaultUserNamespaces = false
			Expect(renderJob(ib, testSource(), cfg).Spec.Template.Spec.HostUsers).To(BeNil())
		})

		It("lets the ImageBuild override the controller default", func() {
			ib := sourceImageBuild()
			ib.Spec.UserNamespaces = new(true)
			cfg := jobConfig
			cfg.DefaultUserNamespaces = false
			Expect(renderJob(ib, testSource(), cfg).Spec.Template.Spec.HostUsers).To(Equal(new(false)))
		})
	})

	Context("with an inline Dockerfile and a source", func() {
		var job *batchv1.Job
		BeforeEach(func() {
			ib := inlineImageBuild()
			ib.Spec.SourceRef = sourceImageBuild().Spec.SourceRef
			job = renderJob(ib, testSource(), jobConfig)
		})

		It("is valid", func() { expectValid(job) })

		It("fetches the source and reads the Dockerfile from the downward API", func() {
			Expect(job.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			Expect(job.Spec.Template.Annotations).To(HaveKeyWithValue(dockerfileAnnotation, "FROM alpine:3.22\nRUN apk add curl\n"))
			args := container(job, BuildContainerName).Args
			Expect(argValues(args, "--local")).To(Equal([]string{contextArg, "dockerfile=/workspace/dockerfile"}))
			Expect(argValues(args, "--opt")).To(ContainElement("filename=Dockerfile"))
			Expect(container(job, BuildContainerName).VolumeMounts).To(ContainElement(corev1.VolumeMount{
				Name: "dockerfile", MountPath: "/workspace/dockerfile", ReadOnly: true,
			}))
			Expect(job.Spec.Template.Spec.Volumes).To(ContainElement(corev1.Volume{
				Name: "dockerfile",
				VolumeSource: corev1.VolumeSource{DownwardAPI: &corev1.DownwardAPIVolumeSource{
					Items: []corev1.DownwardAPIVolumeFile{{Path: dockerfilePath, FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.annotations['build.fluxcd.leightweight.io/dockerfile']",
					}}},
				}},
			}))
		})
	})

	for _, userNS := range []bool{true, false} {
		Context("with an inline Dockerfile and no source", func() {
			var job *batchv1.Job
			BeforeEach(func() {
				ib := inlineImageBuild()
				ib.Spec.UserNamespaces = new(userNS)
				ib.Spec.Context = "ignored"
				job = renderJob(ib, nil, jobConfig)
			})

			It("is valid", func() { expectValid(job) })

			It("has no init container and builds from an empty context", func() {
				Expect(job.Spec.Template.Spec.InitContainers).To(BeEmpty())
				Expect(job.Annotations).NotTo(HaveKey(SourceRevisionAnnotation))
				Expect(job.Annotations).To(HaveKeyWithValue(TagsAnnotation, "build-1760000000"))
				Expect(argValues(container(job, BuildContainerName).Args, "--local")).To(Equal([]string{
					contextArg, "dockerfile=/workspace/dockerfile",
				}))
			})

			It("sets up the matching isolation", func() {
				if userNS {
					Expect(job.Spec.Template.Spec.HostUsers).To(Equal(new(false)))
					Expect(mountPaths(container(job, BuildContainerName))).To(ContainElements("/etc/subuid", "/etc/subgid"))
				} else {
					Expect(job.Spec.Template.Spec.HostUsers).To(BeNil())
					Expect(envValue(container(job, BuildContainerName), "BUILDKITD_FLAGS")).To(Equal("--oci-worker-no-process-sandbox"))
				}
			})
		})
	}

	Context("with other build options", func() {
		It("passes the context subdirectory, Dockerfile path, and target", func() {
			ib := sourceImageBuild()
			ib.Spec.Context = "services/api"
			ib.Spec.Dockerfile.Path = "docker/api.Dockerfile"
			ib.Spec.Target = "runtime"
			args := container(renderJob(ib, testSource(), jobConfig), BuildContainerName).Args
			Expect(argValues(args, "--local")).To(Equal([]string{
				"context=/workspace/context/services/api", "dockerfile=/workspace/context",
			}))
			Expect(argValues(args, "--opt")).To(Equal([]string{"filename=docker/api.Dockerfile", "target=runtime"}))
		})

		It("passes build args in sorted order", func() {
			ib := sourceImageBuild()
			ib.Spec.BuildArgs = map[string]string{"ZED": "z", "ALPHA": "a=b", "MIDDLE": "with space"}
			for range 5 {
				Expect(argValues(container(renderJob(ib, testSource(), jobConfig), BuildContainerName).Args, "--opt")).To(Equal([]string{
					"filename=Dockerfile", "build-arg:ALPHA=a=b", "build-arg:MIDDLE=with space", "build-arg:ZED=z",
				}))
			}
		})

		It("pushes several tags in one quoted name list", func() {
			ib := sourceImageBuild()
			ib.Spec.Push.Tags = []string{latestTag, "{{ .Branch }}-{{ .ShortSHA }}"}
			job := renderJob(ib, testSource(), jobConfig)
			Expect(argValues(container(job, BuildContainerName).Args, "--output")).To(Equal([]string{
				`type=image,"name=` + testImage + `:latest,` + testImage + `:main-0123456",push=true`,
			}))
			Expect(job.Annotations).To(HaveKeyWithValue(TagsAnnotation, "latest,main-0123456"))
		})

		It("marks the registry and cache insecure", func() {
			ib := sourceImageBuild()
			ib.Spec.Push.Insecure = true
			args := container(renderJob(ib, testSource(), jobConfig), BuildContainerName).Args
			Expect(argValues(args, "--output")).To(ConsistOf(HaveSuffix(",push=true,registry.insecure=true")))
			Expect(argValues(args, "--export-cache")).To(ConsistOf(
				"type=registry,ref=" + testImage + ":buildcache,registry.insecure=true,mode=max",
			))
			Expect(argValues(args, "--import-cache")).To(ConsistOf(
				"type=registry,ref=" + testImage + ":buildcache,registry.insecure=true",
			))
		})

		It("leaves out the cache when it's disabled", func() {
			ib := sourceImageBuild()
			ib.Spec.Cache.Enabled = new(false)
			args := container(renderJob(ib, testSource(), jobConfig), BuildContainerName).Args
			Expect(args).NotTo(ContainElements("--export-cache", "--import-cache"))
		})

		It("uses the cache when the setting is unset", func() {
			ib := sourceImageBuild()
			ib.Spec.Cache.Enabled = nil
			Expect(container(renderJob(ib, testSource(), jobConfig), BuildContainerName).Args).To(ContainElement("--export-cache"))
		})

		It("works without registry credentials", func() {
			ib := sourceImageBuild()
			ib.Spec.Push.SecretRef = nil
			job := renderJob(ib, testSource(), jobConfig)
			expectValid(job)
			Expect(hasEnv(container(job, BuildContainerName), "DOCKER_CONFIG")).To(BeFalse())
			Expect(volumeNames(job)).NotTo(ContainElement("docker-config"))
		})

		It("applies resources, node selector, and tolerations to the build pod", func() {
			ib := sourceImageBuild()
			ib.Spec.Resources = &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
			}
			ib.Spec.NodeSelector = map[string]string{"kubernetes.io/arch": "arm64"}
			ib.Spec.Tolerations = []corev1.Toleration{{Key: "builds", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}
			job := renderJob(ib, testSource(), jobConfig)
			expectValid(job)
			Expect(container(job, BuildContainerName).Resources).To(Equal(*ib.Spec.Resources))
			Expect(job.Spec.Template.Spec.NodeSelector).To(Equal(ib.Spec.NodeSelector))
			Expect(job.Spec.Template.Spec.Tolerations).To(Equal(ib.Spec.Tolerations))
		})

		It("returns the tag error instead of a Job", func() {
			ib := sourceImageBuild()
			ib.Spec.Push.Tags = []string{"{{ .Missing }}"}
			_, err := Job(ib, testSource(), jobConfig, testKey, jobTime)
			Expect(err).To(MatchError(ContainSubstring("could not render tag template")))
		})
	})
})

var _ = Describe("JobName", func() {
	It("adds the key prefix to the ImageBuild name", func() {
		Expect(JobName(testName, testKey)).To(Equal("app-0123456789"))
	})

	It("replaces dots so pod hostnames stay valid", func() {
		Expect(JobName("app.v2", testKey)).To(Equal("app-v2-0123456789"))
	})

	It("shortens long names to 63 characters and keeps the key prefix", func() {
		name := JobName(strings.Repeat("a", 253), testKey)
		Expect(name).To(HaveLen(63))
		Expect(name).To(HaveSuffix("-0123456789"))
		Expect(validation.IsDNS1123Label(name)).To(BeEmpty())
	})

	It("doesn't leave a hyphen where it shortens the name", func() {
		name := JobName(strings.Repeat("a", 51)+"-"+strings.Repeat("b", 20), testKey)
		Expect(name).To(Equal(strings.Repeat("a", 51) + "-0123456789"))
		Expect(validation.IsDNS1123Label(name)).To(BeEmpty())
	})

	It("renders a Job the API server accepts for the longest ImageBuild name", func() {
		ib := sourceImageBuild()
		ib.Name = strings.Repeat("a", 253)
		expectValid(renderJob(ib, testSource(), jobConfig))
	})
})

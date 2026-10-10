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
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	buildv1alpha1 "github.com/leightweight/image-build-controller/api/v1alpha1"
)

const (
	// group prefixes the controller's annotations and finalizers. A test checks it against the API group.
	group = "build.fluxcd.leightweight.io"

	// ManagedByLabel and ManagedByValue mark build Jobs and pods, so the controller can watch them.
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedByValue = "image-build-controller"

	// TerminationMessageFinalizer keeps a finished build pod until the controller has read its termination message.
	TerminationMessageFinalizer = group + "/termination-message"

	// BuildKeyAnnotation holds the full build key on a build Job.
	BuildKeyAnnotation = group + "/build-key"
	// SourceRevisionAnnotation holds the source revision a build Job uses, if any.
	SourceRevisionAnnotation = group + "/source-revision"
	// TagsAnnotation holds the comma-separated tags a build Job pushes.
	TagsAnnotation = group + "/tags"
	// ImageAnnotation holds the image name, without a tag, that a build Job pushes.
	ImageAnnotation = group + "/image"
	// dockerfileAnnotation carries an inline Dockerfile into the pod through the downward API.
	dockerfileAnnotation = group + "/dockerfile"
	// subIDsAnnotation carries the subuid and subgid file content into the pod through the downward API.
	subIDsAnnotation = group + "/subids"

	// maxJobNameLength keeps Job names valid as label values, because the Job controller labels pods with them.
	maxJobNameLength = 63

	// BuildContainerName names the build container and the build script's $0.
	BuildContainerName = "build"
	// FetchContainerName names the init container that downloads the source.
	FetchContainerName = "fetch"
	// inlineDockerfileName is the file name an inline Dockerfile gets in the pod.
	inlineDockerfileName = "Dockerfile"

	buildUserID = 1000
	// subIDs maps the build user's subordinate IDs inside the 65536 IDs of a user-namespaced pod.
	// The rootless BuildKit image's own range starts at 100000, outside the pod's IDs.
	subIDs = "user:1001:64535\n"

	contextDir      = "/workspace/context"
	dockerfileDir   = "/workspace/dockerfile"
	outputDir       = "/workspace/output"
	dockerConfigDir = "/workspace/docker-config"
	subIDsDir       = "/workspace/subids"
	tmpDir          = "/tmp"
	buildKitDir     = "/home/user/.local/share/buildkit"
	metadataFile    = outputDir + "/metadata.json"

	contextVolume      = "context"
	dockerfileVolume   = "dockerfile"
	outputVolume       = "output"
	dockerConfigVolume = "docker-config"
	subIDsVolume       = "subids"
	tmpVolume          = "tmp"
	buildKitVolume     = "buildkit"
)

// buildScript runs buildctl with the arguments after it and, on success, writes the pushed image digest as the
// termination message. buildctl writes its metadata file atomically with a rename, which doesn't work on
// /dev/termination-log, so the script copies the digest out of a metadata file in a writable directory.
// On failure the termination message stays empty, and Kubernetes uses the tail of the log instead.
const buildScript = `set -eu
buildctl-daemonless.sh "$@" --metadata-file ` + metadataFile + `
digest="$(sed -n 's/^ *"containerimage.digest": *"\(sha256:[0-9a-f]*\)".*/\1/p' ` + metadataFile + `)"
if [ -z "$digest" ]; then
  echo "build metadata has no image digest" >&2
  exit 1
fi
printf '%s' "$digest" > /dev/termination-log
`

// JobName returns the name of the build Job for an ImageBuild and build key.
// It has the form <name>-<key prefix>, with the ImageBuild name shortened so the whole name fits in 63 characters,
// and dots replaced with hyphens so pod hostnames stay valid.
func JobName(imageBuildName, key string) string {
	suffix := "-" + KeyPrefix(key)
	base := strings.ReplaceAll(imageBuildName, ".", "-")
	if max := maxJobNameLength - len(suffix); len(base) > max {
		base = strings.TrimRight(base[:max], "-")
	}
	return base + suffix
}

// UserNamespaces reports whether a build runs in a user namespace.
func UserNamespaces(ib *buildv1alpha1.ImageBuild, cfg Config) bool {
	if ib.Spec.UserNamespaces != nil {
		return *ib.Spec.UserNamespaces
	}
	return cfg.DefaultUserNamespaces
}

// Job returns the Job that builds and pushes an ImageBuild.
// src is the resolved source artifact, or nil for an inline Dockerfile without a source.
// The Job has a controller owner reference to the ImageBuild, so Kubernetes deletes it with the ImageBuild.
func Job(ib *buildv1alpha1.ImageBuild, src *Source, cfg Config, key string, startedAt time.Time) (*batchv1.Job, error) {
	tags, err := RenderTags(ib.Spec.Push.Tags, NewTagData(src, ib.Generation, startedAt))
	if err != nil {
		return nil, err
	}
	userNS := UserNamespaces(ib, cfg)

	labels := map[string]string{ManagedByLabel: ManagedByValue}
	jobAnnotations := map[string]string{
		BuildKeyAnnotation: key,
		TagsAnnotation:     strings.Join(tags, ","),
		ImageAnnotation:    ib.Spec.Push.Image,
	}
	if src != nil {
		jobAnnotations[SourceRevisionAnnotation] = src.Revision
	}

	podAnnotations := map[string]string{}
	if ib.Spec.Dockerfile.Inline != "" {
		podAnnotations[dockerfileAnnotation] = ib.Spec.Dockerfile.Inline
	}
	if userNS {
		podAnnotations[subIDsAnnotation] = subIDs
	}

	podSpec := corev1.PodSpec{
		RestartPolicy:                 corev1.RestartPolicyNever,
		AutomountServiceAccountToken:  new(false),
		EnableServiceLinks:            new(false),
		NodeSelector:                  ib.Spec.NodeSelector,
		Tolerations:                   ib.Spec.Tolerations,
		TerminationGracePeriodSeconds: ptr.To[int64](30),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsUser:    ptr.To[int64](buildUserID),
			RunAsGroup:   ptr.To[int64](buildUserID),
			RunAsNonRoot: new(true),
		},
		Volumes:    volumes(ib, userNS),
		Containers: []corev1.Container{buildContainer(ib, src, cfg, tags, userNS)},
	}
	if userNS {
		podSpec.HostUsers = new(false)
	}
	if src != nil {
		podSpec.InitContainers = []corev1.Container{fetchContainer(src, cfg)}
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        JobName(ib.Name, key),
			Namespace:   ib.Namespace,
			Labels:      labels,
			Annotations: jobAnnotations,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(ib, buildv1alpha1.GroupVersion.WithKind(buildv1alpha1.ImageBuildKind)),
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: ib.Spec.BackoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: podAnnotations,
					Finalizers:  []string{TerminationMessageFinalizer},
				},
				Spec: podSpec,
			},
		},
	}, nil
}

func fetchContainer(src *Source, cfg Config) corev1.Container {
	return corev1.Container{
		Name:  FetchContainerName,
		Image: cfg.FetchImage,
		Args:  []string{"fetch", "--url", src.URL, "--digest", src.Digest, "--dest", contextDir},
		Env:   []corev1.EnvVar{{Name: "TMPDIR", Value: tmpDir}},
		// A failed fetch reports its error through the termination message, like a failed build.
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: new(false),
			ReadOnlyRootFilesystem:   new(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: contextVolume, MountPath: contextDir},
			{Name: tmpVolume, MountPath: tmpDir},
		},
	}
}

func buildContainer(ib *buildv1alpha1.ImageBuild, src *Source, cfg Config, tags []string, userNS bool) corev1.Container {
	securityContext := &corev1.SecurityContext{
		// rootlesskit needs seccomp and AppArmor unconfined to create its namespaces and mounts.
		SeccompProfile:  &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined},
		AppArmorProfile: &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeUnconfined},
	}
	var env []corev1.EnvVar
	if userNS {
		// RUN steps mount a new /proc, which the kernel refuses inside a masked /proc.
		securityContext.ProcMount = new(corev1.UnmaskedProcMount)
	} else {
		// Without a user namespace, /proc stays masked, so RUN steps share the daemon's process namespace instead.
		env = append(env, corev1.EnvVar{Name: "BUILDKITD_FLAGS", Value: "--oci-worker-no-process-sandbox"})
	}
	if ib.Spec.Push.SecretRef != nil {
		env = append(env, corev1.EnvVar{Name: "DOCKER_CONFIG", Value: dockerConfigDir})
	}

	mounts := []corev1.VolumeMount{
		{Name: contextVolume, MountPath: contextDir, ReadOnly: true},
		{Name: outputVolume, MountPath: outputDir},
		{Name: tmpVolume, MountPath: tmpDir},
		{Name: buildKitVolume, MountPath: buildKitDir},
	}
	if ib.Spec.Dockerfile.Inline != "" {
		mounts = append(mounts, corev1.VolumeMount{Name: dockerfileVolume, MountPath: dockerfileDir, ReadOnly: true})
	}
	if ib.Spec.Push.SecretRef != nil {
		mounts = append(mounts, corev1.VolumeMount{Name: dockerConfigVolume, MountPath: dockerConfigDir, ReadOnly: true})
	}
	if userNS {
		mounts = append(mounts,
			corev1.VolumeMount{Name: subIDsVolume, MountPath: "/etc/subuid", SubPath: "subuid", ReadOnly: true},
			corev1.VolumeMount{Name: subIDsVolume, MountPath: "/etc/subgid", SubPath: "subgid", ReadOnly: true},
		)
	}

	container := corev1.Container{
		Name:                     BuildContainerName,
		Image:                    cfg.BuildKitImage,
		Command:                  []string{"sh", "-c", buildScript, BuildContainerName},
		Args:                     buildctlArgs(ib, src, tags),
		Env:                      env,
		SecurityContext:          securityContext,
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
		VolumeMounts:             mounts,
	}
	if ib.Spec.Resources != nil {
		container.Resources = *ib.Spec.Resources
	}
	return container
}

// buildctlArgs returns the buildctl arguments for an ImageBuild.
// The build script adds --metadata-file.
func buildctlArgs(ib *buildv1alpha1.ImageBuild, src *Source, tags []string) []string {
	spec := ib.Spec

	buildContext := contextDir
	if src != nil && spec.Context != "" {
		buildContext = path.Join(contextDir, spec.Context)
	}
	dockerfileLocal, filename := contextDir, spec.Dockerfile.Path
	if spec.Dockerfile.Inline != "" {
		dockerfileLocal, filename = dockerfileDir, inlineDockerfileName
	}

	args := []string{
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=" + buildContext,
		"--local", "dockerfile=" + dockerfileLocal,
		"--opt", "filename=" + filename,
	}
	if spec.Target != "" {
		args = append(args, "--opt", "target="+spec.Target)
	}
	for _, name := range slices.Sorted(maps.Keys(spec.BuildArgs)) {
		args = append(args, "--opt", fmt.Sprintf("build-arg:%s=%s", name, spec.BuildArgs[name]))
	}

	names := make([]string, len(tags))
	for i, tag := range tags {
		names[i] = spec.Push.Image + ":" + tag
	}
	insecure := ""
	if spec.Push.Insecure {
		insecure = ",registry.insecure=true"
	}
	// The output flag is CSV, so a name list with commas must be one quoted field.
	args = append(args, "--output", fmt.Sprintf(`type=image,"name=%s",push=true%s`, strings.Join(names, ","), insecure))

	if spec.Cache.Enabled == nil || *spec.Cache.Enabled {
		cacheRef := "type=registry,ref=" + spec.Push.Image + ":buildcache" + insecure
		args = append(args,
			"--export-cache", cacheRef+",mode=max",
			"--import-cache", cacheRef,
		)
	}
	return args
}

func volumes(ib *buildv1alpha1.ImageBuild, userNS bool) []corev1.Volume {
	vols := []corev1.Volume{
		{Name: contextVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: outputVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: tmpVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: buildKitVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	if ib.Spec.Dockerfile.Inline != "" {
		vols = append(vols, downwardAPIVolume(dockerfileVolume, map[string]string{inlineDockerfileName: dockerfileAnnotation}))
	}
	if ib.Spec.Push.SecretRef != nil {
		vols = append(vols, corev1.Volume{
			Name: dockerConfigVolume,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: ib.Spec.Push.SecretRef.Name,
				Items:      []corev1.KeyToPath{{Key: corev1.DockerConfigJsonKey, Path: "config.json"}},
			}},
		})
	}
	if userNS {
		vols = append(vols, downwardAPIVolume(subIDsVolume, map[string]string{
			"subuid": subIDsAnnotation,
			"subgid": subIDsAnnotation,
		}))
	}
	return vols
}

// downwardAPIVolume returns a volume with one file per entry in files, each holding a pod annotation's value.
func downwardAPIVolume(name string, files map[string]string) corev1.Volume {
	items := make([]corev1.DownwardAPIVolumeFile, 0, len(files))
	for _, file := range slices.Sorted(maps.Keys(files)) {
		items = append(items, corev1.DownwardAPIVolumeFile{
			Path:     file,
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: fmt.Sprintf("metadata.annotations['%s']", files[file])},
		})
	}
	return corev1.Volume{
		Name:         name,
		VolumeSource: corev1.VolumeSource{DownwardAPI: &corev1.DownwardAPIVolumeSource{Items: items}},
	}
}

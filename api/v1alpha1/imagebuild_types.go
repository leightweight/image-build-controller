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

package v1alpha1

import (
	"github.com/fluxcd/pkg/apis/meta"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ImageBuildKind is the kind name of ImageBuild objects.
const ImageBuildKind = "ImageBuild"

// Reasons the controller sets on ImageBuild conditions, in addition to the reasons in github.com/fluxcd/pkg/apis/meta.
const (
	// BuildFailedReason means the build Job failed after its final attempt.
	BuildFailedReason = "BuildFailed"

	// SourceNotReadyReason means the referenced source has no artifact the controller can build from yet.
	SourceNotReadyReason = "SourceNotReady"
)

// ConcurrencyPolicy controls what happens when a new build is needed while one is running.
// +kubebuilder:validation:Enum=Replace;Wait
type ConcurrencyPolicy string

const (
	// ReplaceConcurrency cancels the running build and starts the new one.
	ReplaceConcurrency ConcurrencyPolicy = "Replace"

	// WaitConcurrency lets the running build finish, then builds the latest state and skips any revisions in between.
	WaitConcurrency ConcurrencyPolicy = "Wait"
)

// BuildResult is the outcome of a finished build.
// +kubebuilder:validation:Enum=Succeeded;Failed
type BuildResult string

const (
	// BuildSucceeded means the build pushed every tag.
	BuildSucceeded BuildResult = "Succeeded"

	// BuildFailed means the build Job failed after its final attempt.
	BuildFailed BuildResult = "Failed"
)

// ImageBuildSpec defines the image to build and where to push it.
// +kubebuilder:validation:XValidation:rule="!has(self.dockerfile.path) || has(self.sourceRef)",message="sourceRef is required when dockerfile.path is set"
// +kubebuilder:validation:XValidation:rule="!has(self.sourceRef) || self.sourceRef.kind == 'GitRepository'",message="sourceRef.kind must be GitRepository"
// +kubebuilder:validation:XValidation:rule="!has(self.sourceRef) || size(self.sourceRef.name) > 0",message="sourceRef.name must not be empty"
type ImageBuildSpec struct {
	// sourceRef references the Flux source to build from.
	// Only GitRepository is supported.
	// It is required when dockerfile.path is set.
	// Without it, the build uses an empty context.
	// +optional
	SourceRef *meta.NamespacedObjectKindReference `json:"sourceRef,omitempty"`

	// dockerfile sets the Dockerfile to build, as a path in the source or as inline contents.
	// +required
	Dockerfile Dockerfile `json:"dockerfile"`

	// context is the subdirectory of the source to use as the build context.
	// It defaults to the source root.
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:XValidation:rule="!self.startsWith('/')",message="context must be a relative path"
	// +kubebuilder:validation:XValidation:rule="!self.split('/').exists(s, s == '..')",message="context must not contain '..'"
	// +optional
	Context string `json:"context,omitempty"`

	// buildArgs are passed to the build as build arguments.
	// +optional
	BuildArgs map[string]string `json:"buildArgs,omitempty"`

	// target is the stage to build in a multi-stage Dockerfile.
	// It defaults to the last stage.
	// +optional
	Target string `json:"target,omitempty"`

	// push sets the image name, tags, and registry credentials.
	// +required
	Push Push `json:"push"`

	// cache configures the registry build cache.
	// +kubebuilder:default={}
	// +optional
	Cache Cache `json:"cache,omitzero"`

	// concurrencyPolicy controls what happens when a new build is needed while one is running.
	// Replace cancels the running build and starts the new one.
	// Wait lets the running build finish, then builds the latest state and skips any revisions in between.
	// +kubebuilder:default=Replace
	// +optional
	ConcurrencyPolicy ConcurrencyPolicy `json:"concurrencyPolicy,omitempty"`

	// rebuildInterval forces a rebuild after this much time has passed since the last build, even if nothing changed.
	// Use it to pick up base image security patches.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(ms|s|m|h))+$"
	// +optional
	RebuildInterval *metav1.Duration `json:"rebuildInterval,omitempty"`

	// userNamespaces runs the build pod with hostUsers set to false, so container users map to unprivileged host users.
	// The nodes must support user namespaces.
	// +optional
	UserNamespaces bool `json:"userNamespaces,omitempty"`

	// resources sets the compute resources of the build container.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// nodeSelector constrains the build pod to nodes with these labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// tolerations lets the build pod schedule onto nodes with matching taints.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// backoffLimit is the number of times the build Job retries a failed build pod before the build fails.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=0
	// +optional
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`

	// successfulBuildsHistoryLimit is the number of finished successful build Jobs to keep.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +optional
	SuccessfulBuildsHistoryLimit *int32 `json:"successfulBuildsHistoryLimit,omitempty"`

	// failedBuildsHistoryLimit is the number of finished failed build Jobs to keep.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=0
	// +optional
	FailedBuildsHistoryLimit *int32 `json:"failedBuildsHistoryLimit,omitempty"`

	// interval is how often the controller re-checks this ImageBuild and corrects drift.
	// It never triggers a build on its own.
	// +kubebuilder:default="10m"
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(ms|s|m|h))+$"
	// +optional
	Interval metav1.Duration `json:"interval,omitzero"`

	// suspend stops the controller from reconciling this ImageBuild.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// Dockerfile sets the Dockerfile to build.
// Set exactly one of path or inline.
// +kubebuilder:validation:XValidation:rule="has(self.path) != has(self.inline)",message="exactly one of path or inline must be set"
type Dockerfile struct {
	// path is the Dockerfile's path relative to the source root.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:XValidation:rule="!self.startsWith('/')",message="path must be a relative path"
	// +kubebuilder:validation:XValidation:rule="!self.split('/').exists(s, s == '..')",message="path must not contain '..'"
	// +optional
	Path string `json:"path,omitempty"`

	// inline holds the Dockerfile contents.
	// +kubebuilder:validation:MinLength=1
	// +optional
	Inline string `json:"inline,omitempty"`
}

// Push sets where the build pushes the image.
// +kubebuilder:validation:XValidation:rule="!has(self.secretRef) || size(self.secretRef.name) > 0",message="secretRef.name must not be empty"
type Push struct {
	// image is the image name without a tag or digest, such as registry.example/foo/bar.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:XValidation:rule="!self.contains('@')",message="image must not include a digest"
	// +kubebuilder:validation:XValidation:rule="!self.split('/')[self.split('/').size() - 1].contains(':')",message="image must not include a tag; set tags instead"
	// +required
	Image string `json:"image"`

	// tags is a list of Go templates, and the build pushes every rendered tag.
	// The templates can use .Branch, .Tag, .SHA, .ShortSHA, .Timestamp, and .Generation.
	// It defaults to {{ .Branch }}-{{ .ShortSHA }}-{{ .Timestamp }}, or to build-{{ .Timestamp }} without a sourceRef.
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Tags []string `json:"tags,omitempty"`

	// secretRef references a kubernetes.io/dockerconfigjson Secret in the ImageBuild namespace with the registry credentials.
	// +optional
	SecretRef *meta.LocalObjectReference `json:"secretRef,omitempty"`

	// insecure allows pushing to a registry over plain HTTP.
	// +optional
	Insecure bool `json:"insecure,omitempty"`
}

// Cache configures the registry build cache.
type Cache struct {
	// enabled stores the build cache in the registry at <image>:buildcache, using the push credentials.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
}

// ImageBuildStatus is the observed state of an ImageBuild.
type ImageBuildStatus struct {
	meta.ReconcileRequestStatus `json:",inline"`

	// observedGeneration is the last generation the controller reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions holds the Ready, Reconciling, and Stalled conditions.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// currentBuild describes the running build, and is present only while a build runs.
	// +optional
	CurrentBuild *CurrentBuild `json:"currentBuild,omitempty"`

	// lastBuild describes the last finished build.
	// +optional
	LastBuild *LastBuild `json:"lastBuild,omitempty"`

	// latestImage is the most recently pushed image, as name@digest.
	// +optional
	LatestImage string `json:"latestImage,omitempty"`
}

// CurrentBuild describes a running build.
type CurrentBuild struct {
	// jobName is the name of the build Job.
	// +required
	JobName string `json:"jobName"`

	// sourceRevision is the source revision being built.
	// +optional
	SourceRevision string `json:"sourceRevision,omitempty"`

	// startedAt is when the build started.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
}

// LastBuild describes a finished build.
type LastBuild struct {
	// buildKey is the hash of the inputs that produced this build.
	// +required
	BuildKey string `json:"buildKey"`

	// sourceRevision is the source revision that was built.
	// +optional
	SourceRevision string `json:"sourceRevision,omitempty"`

	// image lists the pushed tags and the image digest.
	// +optional
	Image *BuiltImage `json:"image,omitempty"`

	// startedAt is when the build started.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// finishedAt is when the build finished.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// jobName is the name of the build Job.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// result is Succeeded or Failed.
	// +required
	Result BuildResult `json:"result"`
}

// BuiltImage describes a pushed image.
type BuiltImage struct {
	// tags are the tags the build pushed.
	// +optional
	Tags []string `json:"tags,omitempty"`

	// digest is the digest of the pushed image.
	// +optional
	Digest string `json:"digest,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ib
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Revision",type=string,JSONPath=`.status.lastBuild.sourceRevision`
// +kubebuilder:printcolumn:name="Latest Image",type=string,JSONPath=`.status.latestImage`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ImageBuild builds a container image from a Flux source or an inline Dockerfile and pushes it to a registry.
type ImageBuild struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the image to build and where to push it.
	// +required
	Spec ImageBuildSpec `json:"spec"`

	// status is the observed state of the ImageBuild.
	// +optional
	Status ImageBuildStatus `json:"status,omitzero"`
}

// GetConditions returns the status conditions of the ImageBuild.
func (in *ImageBuild) GetConditions() []metav1.Condition {
	return in.Status.Conditions
}

// SetConditions sets the status conditions of the ImageBuild.
func (in *ImageBuild) SetConditions(conditions []metav1.Condition) {
	in.Status.Conditions = conditions
}

// +kubebuilder:object:root=true

// ImageBuildList contains a list of ImageBuild.
type ImageBuildList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ImageBuild `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ImageBuild{}, &ImageBuildList{})
		return nil
	})
}

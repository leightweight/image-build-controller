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

// Package build computes build keys, renders image tags, and defines the Job that builds an ImageBuild.
// It never talks to the cluster, so the reconciler supplies everything it needs.
package build

// DefaultBuildKitImage is the rootless BuildKit image each controller release pins.
const DefaultBuildKitImage = "moby/buildkit:v0.34.0-rootless"

// Config holds the controller-wide settings that shape build Jobs.
type Config struct {
	// BuildKitImage runs the build container.
	BuildKitImage string
	// FetchImage runs the fetch init container.
	// It is the controller's own image, which includes the fetch subcommand.
	FetchImage string
	// DefaultUserNamespaces applies to ImageBuilds that don't set spec.userNamespaces.
	DefaultUserNamespaces bool
}

// Source is a resolved Flux source artifact.
type Source struct {
	// URL is the artifact URL from status.artifact.url.
	URL string
	// Digest is the artifact digest from status.artifact.digest.
	Digest string
	// Revision is the artifact revision from status.artifact.revision, such as main@sha1:<hash>.
	Revision string
	// Tag reports whether the source tracks a tag, through spec.ref.tag or spec.ref.semver.
	// Flux revisions don't say whether their name is a branch or a tag, so the caller does.
	Tag bool
}

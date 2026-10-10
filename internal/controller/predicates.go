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
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/fluxcd/pkg/apis/meta"
)

// artifactSource is a Flux source that publishes an artifact.
type artifactSource interface {
	GetArtifact() *meta.Artifact
}

// SourceArtifactChangedPredicate passes source events that can change what an ImageBuild builds from:
// a source created with an artifact, an update to its artifact's revision or digest, and a deleted source.
type SourceArtifactChangedPredicate struct {
	predicate.Funcs
}

// Create passes when the new source already has an artifact.
func (SourceArtifactChangedPredicate) Create(e event.CreateEvent) bool {
	src, ok := e.Object.(artifactSource)
	return ok && src.GetArtifact() != nil
}

// Update passes when the artifact's revision or digest changed, or the artifact appeared or disappeared.
func (SourceArtifactChangedPredicate) Update(e event.UpdateEvent) bool {
	oldSrc, okOld := e.ObjectOld.(artifactSource)
	newSrc, okNew := e.ObjectNew.(artifactSource)
	if !okOld || !okNew {
		return false
	}
	oldArtifact, newArtifact := oldSrc.GetArtifact(), newSrc.GetArtifact()
	if oldArtifact == nil || newArtifact == nil {
		return (oldArtifact == nil) != (newArtifact == nil)
	}
	return oldArtifact.Revision != newArtifact.Revision || oldArtifact.Digest != newArtifact.Digest
}

// Delete passes, so ImageBuilds report that their source is gone.
func (SourceArtifactChangedPredicate) Delete(event.DeleteEvent) bool {
	return true
}

// Generic ignores generic events.
func (SourceArtifactChangedPredicate) Generic(event.GenericEvent) bool {
	return false
}

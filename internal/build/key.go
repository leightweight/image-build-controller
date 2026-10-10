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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/fluxcd/pkg/apis/meta"

	buildv1alpha1 "github.com/leightweight/image-build-controller/api/v1alpha1"
)

// KeyPrefixLength is how many characters of the build key go into Job names.
const KeyPrefixLength = 10

// keyVersion changes whenever the key inputs change, so every ImageBuild rebuilds once after an upgrade
// instead of matching keys computed from different inputs.
const keyVersion = 1

// keyInputs lists everything that goes into a build key.
// Encoding it as JSON gives a stable byte string, because encoding/json sorts map keys.
type keyInputs struct {
	Version          int               `json:"version"`
	SourceDigest     string            `json:"sourceDigest,omitempty"`
	DockerfilePath   string            `json:"dockerfilePath,omitempty"`
	DockerfileInline string            `json:"dockerfileInline,omitempty"`
	Context          string            `json:"context,omitempty"`
	BuildArgs        map[string]string `json:"buildArgs,omitempty"`
	Target           string            `json:"target,omitempty"`
	Image            string            `json:"image"`
	Tags             []string          `json:"tags,omitempty"`
	SecretName       string            `json:"secretName,omitempty"`
	Insecure         bool              `json:"insecure,omitempty"`
	RequestedAt      string            `json:"requestedAt,omitempty"`
	RebuildPeriod    int64             `json:"rebuildPeriod,omitempty"`
}

// Key returns the build key for an ImageBuild and its resolved source, which is nil for builds without a source.
// The controller starts a build only when the key differs from the last recorded one.
//
// The key covers what decides the pushed image and where it goes: the source artifact digest, the Dockerfile,
// context, build args, target, image, tag templates, registry secret name, insecure flag, the
// reconcile.fluxcd.io/requestedAt annotation, and the current rebuildInterval period.
// It leaves out settings that only change how the build runs, such as cache, userNamespaces, resources,
// scheduling, backoffLimit, history limits, concurrencyPolicy, interval, and suspend.
// After changing one of those to fix a failed build, set the requestedAt annotation to retry it.
//
// The key uses the artifact digest rather than its revision, so a commit that leaves the artifact unchanged doesn't rebuild.
func Key(ib *buildv1alpha1.ImageBuild, src *Source, now time.Time) string {
	spec := ib.Spec
	in := keyInputs{
		Version:          keyVersion,
		DockerfilePath:   spec.Dockerfile.Path,
		DockerfileInline: spec.Dockerfile.Inline,
		BuildArgs:        spec.BuildArgs,
		Target:           spec.Target,
		Image:            spec.Push.Image,
		Tags:             spec.Push.Tags,
		Insecure:         spec.Push.Insecure,
	}
	if src != nil {
		in.SourceDigest = src.Digest
		// Without a source, the build context is always empty.
		in.Context = spec.Context
	}
	if spec.Push.SecretRef != nil {
		in.SecretName = spec.Push.SecretRef.Name
	}
	if requestedAt, ok := meta.ReconcileAnnotationValue(ib.GetAnnotations()); ok {
		in.RequestedAt = requestedAt
	}
	if spec.RebuildInterval != nil && spec.RebuildInterval.Duration > 0 {
		in.RebuildPeriod = now.UnixNano() / spec.RebuildInterval.Nanoseconds()
	}

	// Marshalling only fails for unsupported types, and keyInputs has none.
	b, _ := json.Marshal(in)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// KeyPrefix returns the start of a build key, for names and labels.
func KeyPrefix(key string) string {
	if len(key) <= KeyPrefixLength {
		return key
	}
	return key[:KeyPrefixLength]
}

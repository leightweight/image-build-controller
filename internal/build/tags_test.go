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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	sha1Hash   = "0123456789abcdef0123456789abcdef01234567"
	sha256Hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

var tagTime = time.Unix(1760000000, 0)

var _ = Describe("NewTagData", func() {
	DescribeTable("parses Flux revisions",
		func(revision string, tag bool, want TagData) {
			want.Timestamp = tagTime.Unix()
			want.Generation = 4
			Expect(NewTagData(&Source{Revision: revision, Tag: tag}, 4, tagTime)).To(Equal(want))
		},
		Entry("branch", "main@sha1:"+sha1Hash, false,
			TagData{Branch: mainBranch, SHA: sha1Hash, ShortSHA: "0123456"}),
		Entry("branch with slashes", "feature/login@sha1:"+sha1Hash, false,
			TagData{Branch: "feature-login", SHA: sha1Hash, ShortSHA: "0123456"}),
		Entry("tag", "v1.2.3@sha1:"+sha1Hash, true,
			TagData{Tag: "v1.2.3", SHA: sha1Hash, ShortSHA: "0123456"}),
		Entry("full branch reference", "refs/heads/main@sha1:"+sha1Hash, false,
			TagData{Branch: mainBranch, SHA: sha1Hash, ShortSHA: "0123456"}),
		Entry("full tag reference", "refs/tags/v1@sha1:"+sha1Hash, false,
			TagData{Tag: "v1", SHA: sha1Hash, ShortSHA: "0123456"}),
		Entry("pull request reference", "refs/pull/42/head@sha1:"+sha1Hash, false,
			TagData{Branch: "pull-42-head", SHA: sha1Hash, ShortSHA: "0123456"}),
		Entry("commit only", "sha1:"+sha1Hash, false,
			TagData{SHA: sha1Hash, ShortSHA: "0123456"}),
		Entry("HEAD", "HEAD@sha1:"+sha1Hash, false,
			TagData{SHA: sha1Hash, ShortSHA: "0123456"}),
		Entry("SHA-256 repository", "main@sha256:"+sha256Hash, false,
			TagData{Branch: mainBranch, SHA: sha256Hash, ShortSHA: "0123456"}),
		Entry("legacy format", "main/"+sha1Hash, false,
			TagData{Branch: mainBranch, SHA: sha1Hash, ShortSHA: "0123456"}),
		Entry("legacy format with a slash in the branch", "feature/login/"+sha1Hash, false,
			TagData{Branch: "feature-login", SHA: sha1Hash, ShortSHA: "0123456"}),
	)

	It("leaves the source values empty without a source", func() {
		Expect(NewTagData(nil, 2, tagTime)).To(Equal(TagData{Timestamp: tagTime.Unix(), Generation: 2}))
	})
})

var _ = Describe("SanitizeTag", func() {
	It("replaces characters that can't appear in a tag", func() {
		Expect(SanitizeTag("feature/login+fix@2")).To(Equal("feature-login-fix-2"))
	})

	It("keeps valid characters", func() {
		Expect(SanitizeTag("v1.2.3_rc-1")).To(Equal("v1.2.3_rc-1"))
	})
})

var _ = Describe("RenderTags", func() {
	data := TagData{Branch: mainBranch, SHA: sha1Hash, ShortSHA: "0123456", Timestamp: 1760000000, Generation: 4}

	It("renders every template", func() {
		Expect(RenderTags([]string{latestTag, "{{ .Branch }}-{{ .ShortSHA }}", "gen-{{ .Generation }}", "{{ .SHA }}"}, data)).
			To(Equal([]string{latestTag, "main-0123456", "gen-4", sha1Hash}))
	})

	It("removes duplicate tags", func() {
		Expect(RenderTags([]string{latestTag, "{{ .Branch }}", latestTag, mainBranch}, data)).To(Equal([]string{latestTag, mainBranch}))
	})

	DescribeTable("uses default templates when none are set",
		func(data TagData, want string) {
			Expect(RenderTags(nil, data)).To(Equal([]string{want}))
		},
		Entry("branch source", TagData{Branch: mainBranch, ShortSHA: "0123456", SHA: sha1Hash, Timestamp: 1760000000},
			"main-0123456-1760000000"),
		Entry("tag source", TagData{Tag: "v1.2.3", ShortSHA: "0123456", SHA: sha1Hash, Timestamp: 1760000000},
			"v1.2.3-0123456-1760000000"),
		Entry("commit-only source", TagData{ShortSHA: "0123456", SHA: sha1Hash, Timestamp: 1760000000},
			"0123456-1760000000"),
		Entry("no source", TagData{Timestamp: 1760000000}, "build-1760000000"),
	)

	It("gives default tags that a numerical ImagePolicy can sort", func() {
		Expect(RenderTags(nil, data)).To(ConsistOf(MatchRegexp(`^main-[a-f0-9]+-(?P<ts>[0-9]+)$`)))
	})

	DescribeTable("rejects templates that don't render a valid tag",
		func(template, message string) {
			_, err := RenderTags([]string{template}, data)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("syntax error", "{{ .Branch", "could not parse tag template"),
		Entry("unknown field", "{{ .Unknown }}", "could not render tag template"),
		Entry("empty result", "{{ .Tag }}", `rendered "", which isn't a valid image tag`),
		Entry("leading hyphen", "-{{ .Branch }}", "isn't a valid image tag"),
		Entry("invalid character", "{{ .Branch }}:x", "isn't a valid image tag"),
		Entry("longer than 128 characters", strings.Repeat("a", 129), "isn't a valid image tag"),
	)

	It("accepts a 128-character tag", func() {
		Expect(RenderTags([]string{strings.Repeat("a", 128)}, data)).To(HaveLen(1))
	})
})

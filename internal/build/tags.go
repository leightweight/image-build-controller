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
	"regexp"
	"strings"
	"text/template"
	"time"
)

const shortSHALength = 7

var (
	// validTag is the OCI distribution spec's tag grammar.
	validTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	// invalidTagChars matches characters that can't appear in a tag.
	invalidTagChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)
)

// TagData holds the values that push.tags templates can use.
type TagData struct {
	// Branch is the branch from the source revision, sanitized for tags, or empty.
	Branch string
	// Tag is the tag from the source revision, sanitized for tags, or empty.
	Tag string
	// SHA is the full commit hash, without the algorithm prefix.
	SHA string
	// ShortSHA is the first 7 characters of SHA.
	ShortSHA string
	// Timestamp is the Unix time in seconds when the build started.
	Timestamp int64
	// Generation is the ImageBuild's metadata.generation.
	Generation int64
}

// NewTagData parses a Flux source revision into template values.
// A nil source gives empty Branch, Tag, SHA, and ShortSHA.
func NewTagData(src *Source, generation int64, startedAt time.Time) TagData {
	data := TagData{Timestamp: startedAt.Unix(), Generation: generation}
	if src == nil {
		return data
	}

	pointer, hash := splitRevision(src.Revision)
	data.SHA = hash
	data.ShortSHA = hash
	if len(data.ShortSHA) > shortSHALength {
		data.ShortSHA = data.ShortSHA[:shortSHALength]
	}

	switch {
	case pointer == "", pointer == "HEAD":
	case strings.HasPrefix(pointer, "refs/heads/"):
		data.Branch = SanitizeTag(strings.TrimPrefix(pointer, "refs/heads/"))
	case strings.HasPrefix(pointer, "refs/tags/"):
		data.Tag = SanitizeTag(strings.TrimPrefix(pointer, "refs/tags/"))
	case strings.HasPrefix(pointer, "refs/"):
		// Other references, such as refs/pull/1/head, behave like branches.
		data.Branch = SanitizeTag(strings.TrimPrefix(pointer, "refs/"))
	case src.Tag:
		data.Tag = SanitizeTag(pointer)
	default:
		data.Branch = SanitizeTag(pointer)
	}
	return data
}

// splitRevision splits a Flux revision into its named pointer and hash.
// It follows SplitRevision in Flux's pkg/git module without depending on it.
// For example, main@sha1:<hash> gives main and <hash>, sha1:<hash> gives no pointer,
// and the legacy main/<hash> format gives main and <hash>.
func splitRevision(rev string) (pointer, hash string) {
	if i := strings.LastIndex(rev, "@"); i != -1 {
		pointer, rev = rev[:i], rev[i+1:]
	} else if i := strings.LastIndex(rev, "/"); i != -1 && !strings.Contains(rev, ":") {
		pointer, rev = rev[:i], rev[i+1:]
	}
	if i := strings.LastIndex(rev, ":"); i != -1 {
		rev = rev[i+1:]
	}
	return pointer, rev
}

// SanitizeTag replaces every character that can't appear in an image tag with a hyphen.
// For example, the branch feature/login becomes feature-login.
func SanitizeTag(s string) string {
	return invalidTagChars.ReplaceAllString(s, "-")
}

// DefaultTags returns the tag templates for an ImageBuild that doesn't set push.tags.
// The template starts with the branch or tag name when the source revision has one.
func DefaultTags(data TagData) []string {
	switch {
	case data.Branch != "":
		return []string{"{{ .Branch }}-{{ .ShortSHA }}-{{ .Timestamp }}"}
	case data.Tag != "":
		return []string{"{{ .Tag }}-{{ .ShortSHA }}-{{ .Timestamp }}"}
	case data.SHA != "":
		return []string{"{{ .ShortSHA }}-{{ .Timestamp }}"}
	default:
		return []string{"build-{{ .Timestamp }}"}
	}
}

// RenderTags renders tag templates with data and removes duplicates.
// It uses DefaultTags when templates is empty, and returns an error when a template fails or renders an invalid tag.
func RenderTags(templates []string, data TagData) ([]string, error) {
	if len(templates) == 0 {
		templates = DefaultTags(data)
	}

	tags := make([]string, 0, len(templates))
	seen := make(map[string]bool, len(templates))
	for _, text := range templates {
		tmpl, err := template.New("tag").Option("missingkey=error").Parse(text)
		if err != nil {
			return nil, fmt.Errorf("could not parse tag template %q: %w", text, err)
		}
		var b strings.Builder
		if err := tmpl.Execute(&b, data); err != nil {
			return nil, fmt.Errorf("could not render tag template %q: %w", text, err)
		}
		tag := b.String()
		if !validTag.MatchString(tag) {
			return nil, fmt.Errorf("tag template %q rendered %q, which isn't a valid image tag", text, tag)
		}
		if !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	return tags, nil
}

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

package fetch

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	fluxfetch "github.com/fluxcd/pkg/http/fetch"
)

const (
	dockerfileName = "Dockerfile"
	dockerfileBody = "FROM alpine\n"
	urlFlag        = "--url"
)

type tarEntry struct {
	name     string
	body     string
	typeflag byte
	linkname string
}

// archive builds a gzipped tarball from entries and returns it with its sha256 digest.
func archive(entries ...tarEntry) ([]byte, string) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Typeflag: typeflag, Linkname: e.linkname}
		if typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if typeflag == tar.TypeDir {
			hdr.Mode = 0o755
		}
		Expect(tw.WriteHeader(hdr)).To(Succeed())
		if typeflag == tar.TypeReg {
			_, err := tw.Write([]byte(e.body))
			Expect(err).NotTo(HaveOccurred())
		}
	}
	Expect(tw.Close()).To(Succeed())
	Expect(gz.Close()).To(Succeed())
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), "sha256:" + hex.EncodeToString(sum[:])
}

// serve starts a test server that responds to every request with handler and returns the artifact URL.
func serve(handler http.HandlerFunc) string {
	server := httptest.NewServer(handler)
	DeferCleanup(server.Close)
	return server.URL + "/gitrepository/flux-system/app/abc123.tar.gz"
}

func serveArchive(data []byte) string {
	return serve(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	})
}

var _ = Describe("Run", func() {
	var (
		ctx  context.Context
		dest string
	)

	BeforeEach(func() {
		ctx = context.Background()
		dest = filepath.Join(GinkgoT().TempDir(), "context")
	})

	It("extracts the artifact into the destination directory", func() {
		data, digest := archive(
			tarEntry{name: dockerfileName, body: dockerfileBody},
			tarEntry{name: "app/", typeflag: tar.TypeDir},
			tarEntry{name: "app/main.go", body: "package main\n"},
		)

		Expect(Run(ctx, Options{URL: serveArchive(data), Digest: digest, Dir: dest})).To(Succeed())

		Expect(os.ReadFile(filepath.Join(dest, dockerfileName))).To(BeEquivalentTo(dockerfileBody))
		Expect(os.ReadFile(filepath.Join(dest, "app", "main.go"))).To(BeEquivalentTo("package main\n"))
	})

	It("accepts a digest without an algorithm prefix, as Flux does", func() {
		data, digest := archive(tarEntry{name: dockerfileName, body: dockerfileBody})

		Expect(Run(ctx, Options{URL: serveArchive(data), Digest: digest[len("sha256:"):], Dir: dest})).To(Succeed())
		Expect(filepath.Join(dest, dockerfileName)).To(BeAnExistingFile())
	})

	It("rejects an artifact whose digest doesn't match and extracts nothing", func() {
		data, _ := archive(tarEntry{name: dockerfileName, body: dockerfileBody})
		_, otherDigest := archive(tarEntry{name: dockerfileName, body: "FROM busybox\n"})

		err := Run(ctx, Options{URL: serveArchive(data), Digest: otherDigest, Dir: dest})

		Expect(err).To(MatchError(ContainSubstring("computed digest doesn't match")))
		Expect(os.ReadDir(dest)).To(BeEmpty())
	})

	It("reports a missing artifact", func() {
		url := serve(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})

		err := Run(ctx, Options{URL: url, Digest: "sha256:" + hex.EncodeToString(make([]byte, 32)), Dir: dest})

		Expect(err).To(MatchError(fluxfetch.ErrFileNotFound))
		Expect(err).To(MatchError(ContainSubstring("was not found")))
	})

	It("retries a server error and then succeeds", func(specCtx SpecContext) {
		data, digest := archive(tarEntry{name: dockerfileName, body: dockerfileBody})
		var requests atomic.Int32
		url := serve(func(w http.ResponseWriter, _ *http.Request) {
			if requests.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write(data)
		})

		// The Flux fetcher waits at least 5 seconds between attempts.
		Expect(Run(specCtx, Options{URL: url, Digest: digest, Dir: dest, Retries: 1})).To(Succeed())

		Expect(requests.Load()).To(BeEquivalentTo(2))
		Expect(filepath.Join(dest, dockerfileName)).To(BeAnExistingFile())
	}, SpecTimeout(30*time.Second))

	It("fails after the server keeps returning errors", func() {
		var requests atomic.Int32
		url := serve(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		})

		err := Run(ctx, Options{URL: url, Digest: "sha256:" + hex.EncodeToString(make([]byte, 32)), Dir: dest})

		Expect(err).To(MatchError(ContainSubstring("could not fetch artifact")))
		Expect(requests.Load()).To(BeEquivalentTo(1))
	})

	It("does not create symlinks from the artifact", func() {
		data, digest := archive(
			tarEntry{name: dockerfileName, body: dockerfileBody},
			tarEntry{name: "passwd", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
		)

		Expect(Run(ctx, Options{URL: serveArchive(data), Digest: digest, Dir: dest})).To(Succeed())

		Expect(filepath.Join(dest, dockerfileName)).To(BeAnExistingFile())
		_, err := os.Lstat(filepath.Join(dest, "passwd"))
		Expect(os.IsNotExist(err)).To(BeTrue(), "the symlink should not exist")
	})

	It("never writes outside the destination directory", func() {
		data, digest := archive(tarEntry{name: "../escaped", body: "oops\n"})

		// Whether the extractor rejects the entry or drops it, nothing may land next to the destination.
		_ = Run(ctx, Options{URL: serveArchive(data), Digest: digest, Dir: dest})

		Expect(filepath.Join(filepath.Dir(dest), "escaped")).NotTo(BeAnExistingFile())
	})

	DescribeTable("requires its inputs",
		func(opts Options, message string) {
			Expect(Run(ctx, opts)).To(MatchError(message))
		},
		Entry("URL", Options{Digest: "sha256:abc", Dir: "/tmp/x"}, "an artifact URL is required"),
		Entry("digest", Options{URL: "http://example.invalid/a.tar.gz", Dir: "/tmp/x"}, "an artifact digest is required"),
		Entry("destination", Options{URL: "http://example.invalid/a.tar.gz", Digest: "sha256:abc"},
			"a destination directory is required"),
	)
})

var _ = Describe("Main", func() {
	It("fetches with the values from its flags", func() {
		data, digest := archive(tarEntry{name: dockerfileName, body: dockerfileBody})
		dest := filepath.Join(GinkgoT().TempDir(), "context")
		var stderr bytes.Buffer

		code := Main(context.Background(), []string{
			urlFlag, serveArchive(data), "--digest", digest, "--dest", dest, "--retries", "0",
		}, &stderr)

		Expect(code).To(Equal(0), stderr.String())
		Expect(filepath.Join(dest, dockerfileName)).To(BeAnExistingFile())
	})

	It("exits with 1 and explains why when the fetch fails", func() {
		data, _ := archive(tarEntry{name: dockerfileName, body: dockerfileBody})
		var stderr bytes.Buffer

		code := Main(context.Background(), []string{
			urlFlag, serveArchive(data), "--digest", "sha256:" + hex.EncodeToString(make([]byte, 32)),
			"--dest", GinkgoT().TempDir(),
		}, &stderr)

		Expect(code).To(Equal(1))
		Expect(stderr.String()).To(ContainSubstring("fetch failed: "))
	})

	It("exits with 2 on an unknown flag", func() {
		var stderr bytes.Buffer
		Expect(Main(context.Background(), []string{"--bogus"}, &stderr)).To(Equal(2))
		Expect(stderr.String()).To(ContainSubstring("flag provided but not defined: -bogus"))
	})

	It("exits with 2 on unexpected arguments", func() {
		var stderr bytes.Buffer
		Expect(Main(context.Background(), []string{urlFlag, "x", "extra"}, &stderr)).To(Equal(2))
		Expect(stderr.String()).To(ContainSubstring("unexpected arguments: [extra]"))
	})
})

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

// Package fetch implements the fetch subcommand, which runs in the build pod's init container.
// It downloads a Flux source artifact, verifies its digest, and extracts it into the build context directory.
package fetch

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/go-logr/logr"

	fluxfetch "github.com/fluxcd/pkg/http/fetch"
	"github.com/fluxcd/pkg/tar"
)

const (
	// DefaultRetries matches the default artifact fetch retries of the Flux controllers.
	DefaultRetries = 9
)

// Options configures a fetch.
type Options struct {
	// URL is the artifact URL from the source's status.artifact.url.
	URL string
	// Digest is the artifact digest from the source's status.artifact.digest.
	Digest string
	// Dir is the directory to extract the artifact into.
	Dir string
	// Retries is how many times to retry a failed download.
	Retries int
}

// Run downloads the artifact, verifies its digest, and extracts it into opts.Dir.
// Retry messages go to the logger in ctx, if any.
func Run(ctx context.Context, opts Options) error {
	if opts.URL == "" {
		return errors.New("an artifact URL is required")
	}
	if opts.Digest == "" {
		return errors.New("an artifact digest is required")
	}
	if opts.Dir == "" {
		return errors.New("a destination directory is required")
	}

	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return fmt.Errorf("could not create destination directory: %w", err)
	}

	fetcher := fluxfetch.New(
		fluxfetch.WithRetries(opts.Retries),
		fluxfetch.WithMaxDownloadSize(tar.UnlimitedUntarSize),
		fluxfetch.WithUntar(tar.WithMaxUntarSize(tar.UnlimitedUntarSize)),
		fluxfetch.WithLogger(logr.FromContextOrDiscard(ctx)),
	)
	if err := fetcher.FetchWithContext(ctx, opts.URL, opts.Digest, opts.Dir); err != nil {
		if errors.Is(err, fluxfetch.ErrFileNotFound) {
			return fmt.Errorf("artifact %s was not found: %w", opts.URL, err)
		}
		return fmt.Errorf("could not fetch artifact %s: %w", opts.URL, err)
	}
	return nil
}

// Main parses the fetch subcommand's arguments, runs it, and returns the process exit code.
func Main(ctx context.Context, args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("fetch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var opts Options
	flags.StringVar(&opts.URL, "url", "", "The artifact URL from the source's status.artifact.url.")
	flags.StringVar(&opts.Digest, "digest", "", "The artifact digest from the source's status.artifact.digest.")
	flags.StringVar(&opts.Dir, "dest", "", "The directory to extract the artifact into.")
	flags.IntVar(&opts.Retries, "retries", DefaultRetries, "How many times to retry a failed download.")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected arguments: %v\n", flags.Args())
		return 2
	}

	if err := Run(ctx, opts); err != nil {
		_, _ = fmt.Fprintf(stderr, "fetch failed: %v\n", err)
		return 1
	}
	return 0
}

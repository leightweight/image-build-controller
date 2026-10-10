---
paths:
  - "**/*.go"
---

# Go

- Do not add `//nolint` directives or disable golangci-lint rules.
  Change the code until it passes.
- If the code needs a refactor before a change can pass lint, put the refactor in its own commit or PR first.
- `go.mod` repeats Flux's `replace` for `github.com/opencontainers/go-digest`, because Go ignores `replace` directives in dependencies.
  When you update a `github.com/fluxcd/pkg` module, compare the line with kustomize-controller's `go.mod` and keep them the same.

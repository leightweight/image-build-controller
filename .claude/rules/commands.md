# Commands

Run everything through mise tasks.
If a task you need doesn't exist, add it to `.config/mise/config.toml` and list it in this file instead of running the command directly.

## Setup

- `mise bootstrap`: set up a fresh checkout.
  It runs the `bootstrap` task, which installs the git hooks.
- Before `mise run` and `mise exec`, mise runs these `[deps]` providers when their inputs change:
  - `go` downloads Go module dependencies.
  - `golangci-lint` builds golangci-lint with the plugins in `.custom-gcl.yml` into `bin/golangci-lint`.

## Development

- `mise run generate`: regenerate deepcopy code, CRDs, and RBAC from the API types and kubebuilder markers.
- `mise run build`: build the controller into `bin/manager`.
- `mise run run`: run the controller against the current kubeconfig context.
- `mise run chart`: regenerate the Helm chart in `charts/chart` from the Kustomize config.
  Run it after changing anything in `config/`, including through `mise run generate`.
  CI fails when the committed chart is out of date.
- `mise run lint`: run all linters.
- `mise run lint-fix` (alias `fix`): run all linters and apply fixes.
- `mise run kubebuilder <args>`: run the kubebuilder CLI.
  For `create` commands, it skips kubebuilder's `make generate` step and runs `mise run generate` instead.
  Put `--` before `--help`, as in `mise run kubebuilder -- create api --help`, or mise shows its own help.

## Testing

- `mise run test`: run unit and controller tests with envtest.
  The envtest binaries go in `bin/k8s/`, where the generated test suites also look when an IDE runs them.
  The coverage profile goes in `.cache/cover.out`.
- `mise run test:e2e`: regenerate the chart, create a kind cluster, run the end-to-end tests, and delete the cluster.
  The tests install the controller with the Helm chart.
  Pass `--keep-cluster` to keep it for debugging.

## Cluster

These tasks act on the current kubeconfig context.

- `mise run docker-build [image]`: build the controller image.
- `mise run install` and `mise run uninstall`: add or remove the CRDs.
- `mise run deploy [image]` and `mise run undeploy`: add or remove the whole controller.

## Releases

- `mise run installer <version>`: build the release `install.yaml` into `.cache/release/`.
  goreleaser runs it before each release.
- `mise run release:snapshot`: build the release binaries and per-platform images locally without publishing them.
  The output goes in `.cache/dist/`.
- Pushing a `v*` tag publishes a release through `.github/workflows/release.yml`.
  Only push release tags when the user asks.

## Configuration

`.config/mise/config.toml` pins the tool versions and defines most tasks.
File tasks live in `.config/mise/tasks/`.
Linter configuration lives in `.config/hk.pkl`, `.config/rumdl.toml`, `.golangci.yml`, and `.custom-gcl.yml`.
The `helm` step in `.config/hk.pkl` runs `helm lint --strict` on the chart, and the `goreleaser` step runs `goreleaser check`.
The release configuration lives in `.goreleaser.yaml` and `Dockerfile.release`.
When you change the golangci-lint version in `[tools]`, change `version` in `.custom-gcl.yml` to match.

`.github/renovate.json5` configures Renovate, which opens dependency update pull requests weekly.
When you pin a new image in Go code, name the constant with an `Image` suffix and add its file to the Go image manager in that config.

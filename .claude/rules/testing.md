# Testing

- Tests use Ginkgo and Gomega.
  Each package's `suite_test.go` sets up the test environment.
- Unit and controller tests use envtest, which runs a real API server and etcd without a cluster.
- End-to-end tests run real builds on kind with Flux source-controller, a Gitea server, and a local registry.
  The manifests for those services live in `test/e2e/testdata/`.
  The tests create their Git commits through the Gitea API, so they need no fixture repository.
  Run them only against the dedicated kind cluster that `mise run test:e2e` creates, never a real cluster.
- Always run tests through mise tasks, never by running test scripts or binaries directly.
- End-to-end tests are slow.
  Run them only for changes to build behavior, or when the user asks.

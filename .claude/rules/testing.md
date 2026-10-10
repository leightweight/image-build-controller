# Testing

- Unit and controller tests use envtest.
- End-to-end tests run real builds on kind with Flux and a local registry.
- Always run tests through mise tasks, never by running test scripts or binaries directly.
- End-to-end tests are slow.
  Run them only for changes to build behavior, or when the user asks.

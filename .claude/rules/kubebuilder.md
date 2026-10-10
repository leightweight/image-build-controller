---
paths:
  - "api/**"
  - "charts/**"
  - "cmd/**"
  - "config/**"
  - "internal/**"
  - "test/**"
  - "PROJECT"
---

# Kubebuilder

- Create new APIs, controllers, and webhooks with `mise run kubebuilder create ...`, not by hand.
  The CLI also updates `PROJECT`, `cmd/main.go`, the test suites, and `config/`.
  The task passes `--make=false` and runs `mise run generate` afterwards, because this project has no Makefile.
- Do not hand-edit `PROJECT`.
  The kubebuilder CLI maintains it.
- Do not delete `// +kubebuilder:scaffold:*` comments.
  The CLI adds code at those markers.
- Keep files where kubebuilder put them, because the CLI expects that layout.
- `mise run chart` generates the Helm chart in `charts/chart` with the `helm/v2-alpha` plugin.
  Change generated templates through `config/` and regenerate them, never by hand.
  Hand edits go only in `Chart.yaml`, `values.yaml`, and `templates/extras/`, which the plugin keeps.
- Watch types from other projects, such as Flux's `GitRepository`, from the `ImageBuild` controller with `Watches()`.
  Import their Go API module and register it in the manager's scheme.
  Don't scaffold a separate controller for them with `kubebuilder create api --resource=false`.

The [Kubebuilder book](https://book.kubebuilder.io) and its [good practices](https://book.kubebuilder.io/reference/good-practices.html) page cover the patterns behind these rules.

---
paths:
  - "api/**"
  - "config/crd/**"
  - "config/rbac/**"
  - "**/zz_generated.*.go"
---

# API and generated code

## Generated files

Do not hand-edit these files.
`mise run generate` produces them from the Go types and kubebuilder markers, so change those and regenerate.

- `config/crd/bases/*.yaml`
- `config/rbac/role.yaml`
- `**/zz_generated.*.go`

After changing anything under `api/` or any kubebuilder marker, run `mise run generate` and commit the regenerated files with the change.

## API design

- Do not add or change API fields without updating the [API section of the design doc](../docs/design.md#api).
- Follow the [Kubernetes API conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md).
- Use `metav1.Condition` for status conditions, not custom string fields.
- Use `metav1.Time` and `metav1.Duration` for times and durations, not strings.
- Put validation and defaults in kubebuilder markers, such as `+kubebuilder:validation:Pattern` and `+kubebuilder:default`.
  See the [markers reference](https://book.kubebuilder.io/reference/markers.html).

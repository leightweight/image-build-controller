---
paths:
  - "api/**"
  - "config/crd/**"
  - "config/rbac/**"
  - "**/zz_generated.*.go"
---

# API and generated code

- Do not hand-edit generated code.
  controller-gen generates CRDs, RBAC manifests, and `zz_generated.*` files from Go types and kubebuilder markers, so change those and regenerate.
- After changing anything under `api/` or any kubebuilder marker, run the generate task and commit the regenerated files with the change.
- Do not add or change API fields without updating the [API section of the design doc](../docs/design.md#api).

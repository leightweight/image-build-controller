# Principles

- [`.claude/docs/design.md`](../docs/design.md) is the source of truth for the API, build lifecycle, and security model.
  Read it before changing behavior, and update it in the same change when the design moves.
- Use maintained open source dependencies instead of writing our own, so the project has less code to maintain.
  Before implementing anything generic, such as conditions, patching, artifact fetching, or retries, check whether controller-runtime or `fluxcd/pkg` already provides it.
- Follow Flux controller conventions: Flux condition types, `spec.interval`, `spec.suspend`, the `reconcile.fluxcd.io/requestedAt` annotation, and kstatus-compatible status.
- The controller never reads Secrets.
  Build pods mount registry credentials directly.
  Do not add RBAC or code that gives the controller access to Secrets.

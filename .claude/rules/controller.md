---
paths:
  - "internal/**/*.go"
  - "cmd/**/*.go"
---

# Controller

## Build state

- Do not assume a source exists.
  Inline-Dockerfile builds have no `sourceRef`, so values derived from the revision, such as the branch and SHA, can be empty.
- Do not keep build state only in controller memory.
  The controller must be able to resume any build from the `ImageBuild` status and its Jobs after a restart.

## Reconciliation

- Reconciliation must be idempotent, so running it twice with the same state changes nothing the second time.
- Set owner references with `controllerutil.SetControllerReference` on every object the controller creates, so Kubernetes deletes them with their `ImageBuild`.
- Watch the resources the controller depends on with `Owns()` or `Watches()`.
  Use `RequeueAfter` only for time-based work, such as `spec.interval` and `rebuildInterval`, not to poll for changes.
- Declare RBAC with `+kubebuilder:rbac` markers on the reconciler, and keep them to the permissions in the [design doc](../docs/design.md#controller-rbac).

## Logging

Get the logger with `log.FromContext(ctx)` and follow the [Kubernetes log message style](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md#message-style-guidelines):

- Start the message with a capital letter, and don't end it with a period.
- Use the past tense, such as "Could not delete Pod", not "Cannot delete Pod".
- Name the object type, such as "Deleted Job", not "Deleted".
- Pass balanced key-value pairs after the message.
  The golangci-lint `logcheck` plugin checks this.

```go
log.Info("Created Job", "job", klog.KObj(job))
log.Error(err, "Failed to create Job", "imagebuild", klog.KObj(ib))
```

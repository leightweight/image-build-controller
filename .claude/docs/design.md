# image-build-controller design

## Summary

image-build-controller is a Kubernetes controller that builds container images for Flux.
It builds an image from a Flux `GitRepository` or an inline Dockerfile and pushes it to a registry.
Flux image automation then detects the new tag and deploys it.

The main use case is a homelab running open source projects that don't publish a pre-built image.
Flux already fetches the git repository, detects changes, updates image tags, and reconciles the cluster.
Flux can't build an image, so this controller does that without a CI pipeline per project.

Other people should be able to install and use it, so it must not depend on anything specific to the author's homelab.

## Project

- Repository: `github.com/leightweight/image-build-controller`.
- Go module: `github.com/leightweight/image-build-controller`.
- API group and version: `image-build.leightweight.io/v1alpha1`.
- Kind: `ImageBuild`.
- License: Apache-2.0, matching Flux and Kubernetes.
- Source file copyright header: `Copyright <year> The image-build-controller Authors`.

## Principles

- Use maintained open source dependencies instead of writing our own, so the project has less code to maintain.
- Follow Flux controller conventions wherever possible.
- Keep all state needed to resume work in the cluster, never only in controller memory.
- The controller never reads Secrets.

## API

```yaml
apiVersion: image-build.leightweight.io/v1alpha1
kind: ImageBuild
metadata:
  name: foo
  namespace: apps
spec:
  # Optional when dockerfile.inline is set; required when dockerfile.path is set.
  sourceRef:
    kind: GitRepository
    name: foo
    namespace: flux-system
  # Exactly one of path or inline.
  dockerfile:
    path: Dockerfile
    # inline: |
    #   FROM alpine
  context: ./subdir
  buildArgs:
    KEY: value
  target: runtime
  push:
    image: registry.example/foo/bar
    tags:
      - '{{ .Branch }}-{{ .ShortSHA }}-{{ .Timestamp }}'
    secretRef:
      name: regcreds
    insecure: false
  cache:
    enabled: true
  concurrencyPolicy: Replace
  rebuildInterval: 168h
  userNamespaces: false
  resources: {}
  nodeSelector: {}
  tolerations: []
  backoffLimit: 2
  successfulBuildsHistoryLimit: 1
  failedBuildsHistoryLimit: 3
  interval: 10m
  suspend: false
```

### Source

`sourceRef` has the same shape as Flux source references: `kind`, `name`, and optional `namespace`.
Only `GitRepository` is supported in v1.
`OCIRepository` and `Bucket` publish artifacts the same way, so we can add them later without changing the API.

### Dockerfile and context

`dockerfile` takes exactly one of `path` or `inline`.
`path` is relative to the source root, and `inline` holds the Dockerfile contents.
`context` is an optional subdirectory of the source to use as the build context.
With `inline` and a `sourceRef`, the source is the build context.
With `inline` and no `sourceRef`, the build uses an empty context, and only changes to the `ImageBuild` trigger rebuilds.
BuildKit applies `.dockerignore` files without extra configuration.

### Push and tags

`push.image` is the image name without a tag.
`push.tags` is a list of Go templates.
The build pushes every rendered tag.

Template variables:

- `.Branch`: the branch from the source revision, if any.
- `.Tag`: the git tag from the source revision, if any.
- `.SHA`: the full commit SHA.
- `.ShortSHA`: the abbreviated commit SHA.
- `.Timestamp`: Unix time when the build started.
- `.Generation`: the `ImageBuild` generation.

The default tag is `{{ .Branch }}-{{ .ShortSHA }}-{{ .Timestamp }}`.
The default tag without a source is `build-{{ .Timestamp }}`.

A Flux `ImagePolicy` like this one selects the newest default tag:

```yaml
filterTags:
  pattern: '^main-[a-f0-9]+-(?P<ts>[0-9]+)$'
  extract: '$ts'
policy:
  numerical:
    order: asc
```

`push.secretRef` references a `kubernetes.io/dockerconfigjson` Secret in the `ImageBuild` namespace, the same format as `imagePullSecrets`.
That one format works for GHCR, Docker Hub, and in-cluster registries.
`push.insecure` allows plain HTTP registries, such as an in-cluster registry.

### Build options

- `buildArgs`: passed to BuildKit as build arguments.
- `target`: the multi-stage build target.
- `cache.enabled`: defaults to true and stores the registry cache at `<image>:buildcache` with the push credentials.
- `resources`, `nodeSelector`, and `tolerations`: applied to the build pod.
- `userNamespaces`: off by default and sets `hostUsers: false` on the build pod when enabled.
  See [User namespaces](#user-namespaces).

### Concurrency

`concurrencyPolicy` controls what happens when a new build is needed while one is running.

- `Replace` (default): cancel the running build and start the new one.
- `Wait`: let the running build finish, then build the latest state and skip any revisions in between.

### Flux conventions

- `spec.interval`: how often the controller re-checks the resource and corrects drift.
  It never triggers a build on its own.
- `spec.suspend`: pauses reconciliation.
- The `reconcile.fluxcd.io/requestedAt` annotation forces a rebuild.
- Status conditions follow kstatus, so Flux `Kustomization` health checks can wait on an `ImageBuild`.

### Status

```yaml
status:
  observedGeneration: 3
  lastHandledReconcileAt: "..."
  conditions:
    - type: Ready
    - type: Reconciling
    - type: Stalled
  currentBuild:
    jobName: foo-1a2b3c4d
    sourceRevision: main@sha1:...
    startedAt: "..."
  lastBuild:
    buildKey: 1a2b3c4d...
    sourceRevision: main@sha1:...
    image:
      tags: [main-abc1234-1760000000]
      digest: sha256:...
    startedAt: "..."
    finishedAt: "..."
    jobName: foo-1a2b3c4d
    result: Succeeded
  latestImage: registry.example/foo/bar@sha256:...
```

`currentBuild` is present only while a build is running.
`kubectl get imagebuilds` shows Ready, Revision, Latest Image, and Age columns.

## Build lifecycle

### Build key

The controller computes a build key as a hash of:

- the source artifact digest;
- every spec field that affects the produced image;
- the `reconcile.fluxcd.io/requestedAt` value;
- the current `rebuildInterval` period, if set.

The controller starts a build only when the build key differs from the last recorded one.
Controller restarts, resyncs, and changes to `interval` or `suspend` never trigger rebuilds.
The controller does not check whether the image still exists in the registry.

### Triggers

- The referenced `GitRepository` publishes a new artifact.
- A spec field that affects the image changes.
- The `reconcile.fluxcd.io/requestedAt` annotation changes.
- `rebuildInterval` elapses, so the image picks up base image security patches.

If the source is not ready, the `ImageBuild` reports `Ready=False` and waits.

### Build Job

Each build is a Job in the `ImageBuild` namespace, owned by the `ImageBuild`.
The Job name is `<name>-<build key prefix>`, truncated to fit Kubernetes name limits.
After a restart, the controller finds and adopts an existing Job for the current build key instead of creating a duplicate.

The pod has two containers:

1. An init container that runs the controller image with the `fetch` subcommand.
   It uses `fluxcd/pkg/http/fetch` to download the Flux source artifact, verify its digest, and extract it into an `emptyDir` volume.
2. A build container that runs rootless BuildKit in daemonless mode with `buildctl-daemonless.sh`.
   It builds from the extracted context and pushes to the registry.

A controller flag sets the BuildKit image, and each controller release pins a default.

### Failure handling

- The Job `backoffLimit` (default 2) retries transient failures such as registry timeouts.
- After the final attempt fails, the `ImageBuild` reports `Ready=False` and `Stalled=True` with reason `BuildFailed`.
- The controller never retries a failed build key on its own.
  A new revision, a spec change, or `requestedAt` starts a new build.
- The build container uses `terminationMessagePolicy: FallbackToLogsOnError`.
  Kubernetes copies the tail of the build log into the termination message, and the controller puts it in the condition message and a Kubernetes Event without needing permission to read logs.

### History

Kubernetes deletes a build's Jobs through owner references when the `ImageBuild` is deleted.
`successfulBuildsHistoryLimit` (default 1) and `failedBuildsHistoryLimit` (default 3) limit how many finished Jobs the controller keeps, the same way `CronJob` does.

## Security

### Build pods

Rootless BuildKit requires `seccomp` and `apparmor` to be Unconfined.
The baseline Pod Security Standard rejects that, so namespaces running builds must allow the `privileged` level.
The install docs must state this requirement.

Build pods have these restrictions:

- run as non-root (UID 1000);
- no service account token mounted;
- read-only root filesystem except for the BuildKit state volume.

### User namespaces

`userNamespaces: true` sets `hostUsers: false`, which maps container users to unprivileged host UIDs.
If a build escapes its container, it lands on the node as an unprivileged user.
It does not remove the `privileged` Pod Security requirement.
It requires Kubernetes 1.33 or later, Linux 6.3 or later, and containerd 2.0 or later or CRI-O on the nodes.
It is off by default, and a controller flag changes the default.
Support depends on a test confirming that rootless BuildKit works inside a user namespace, possibly with `--oci-worker-no-process-sandbox`.
If it doesn't work, we drop the field and document why.

### Network access to source-controller

By default, `flux bootstrap` installs NetworkPolicies that only allow pods in `flux-system` to reach source-controller.
Build pods in other namespaces cannot download artifacts without an extra policy.
The Helm chart includes an optional NetworkPolicy that allows traffic to the source-controller artifact port from pods labelled `app.kubernetes.io/managed-by: image-build-controller` in any namespace.

### Controller RBAC

The controller watches `ImageBuild` resources across the whole cluster with these permissions:

- `ImageBuild`: get, list, watch, update, patch, and update status;
- `Job`: create, delete, get, list, and watch;
- `GitRepository`: get, list, and watch;
- `Event`: create and patch.

The controller never reads Secrets.
Build pods mount the registry Secret directly.

### Controller flags

- The default BuildKit image.
- The default for `userNamespaces`.
- Restricting the controller to a single namespace.
- `--no-cross-namespace-refs`, which rejects `sourceRef`s in other namespaces, as Flux does.
- An optional notification-controller events address.

## Implementation

- Project skeleton: kubebuilder, for controller-runtime, CRD and RBAC generation, and envtest.
- Flux runtime: `fluxcd/pkg/runtime` for conditions, patch helpers, event recording, and `requestedAt` handling.
- Source types: `fluxcd/source-controller/api`.
- Artifact fetching: `fluxcd/pkg/http/fetch`.
- Build backend: rootless BuildKit behind a small internal interface.
  The API exposes only BuildKit.

## Distribution

- Helm chart: the main documented install method, published as an OCI artifact to `ghcr.io/leightweight`.
- `install.yaml`: a release asset generated from the kubebuilder Kustomize config, available but not the main documented method.
- Controller images: multi-arch, built and published by goreleaser.

## CI

GitHub Actions runs:

- golangci-lint;
- unit and controller tests with envtest;
- an end-to-end test on kind that builds a real image with Flux and a local registry.

## Deferred

- Flux notifications through notification-controller.
  The author already uses Flux notifications, so this comes first after v1.
  The `Alert` API may restrict event source kinds, so this needs verification.
  For v1, the controller emits standard Kubernetes Events only.
- Build secrets (`--secret`).
- Multi-arch image builds.
- Additional build backends.
- Rebuilding when a base image digest changes.
- Per-`ImageBuild` BuildKit image overrides.
- Cloud workload identity for registry authentication.
- `OCIRepository` and `Bucket` sources.

## Open risks

1. Rootless BuildKit inside user-namespaced pods.
2. Whether notification-controller accepts events from custom kinds.

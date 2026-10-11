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
- API group and version: `build.fluxcd.leightweight.io/v1alpha1`.
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
apiVersion: build.fluxcd.leightweight.io/v1alpha1
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
  userNamespaces: true
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

`sourceRef` uses the Flux `NamespacedObjectKindReference` type: `kind`, `name`, and optional `namespace` and `apiVersion`.
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
- `.ShortSHA`: the first 7 characters of the commit SHA.
- `.Timestamp`: Unix time when the build started.
- `.Generation`: the `ImageBuild` generation.

Flux revisions such as `main@sha1:<sha>` don't say whether their name is a branch or a tag.
The controller treats the name as a tag when the `GitRepository` sets `spec.ref.tag` or `spec.ref.semver`, and as a branch otherwise.
Full references are unambiguous: `refs/heads/<name>` is a branch, `refs/tags/<name>` is a tag, and other references such as `refs/pull/1/head` act as branches named without the `refs/` prefix.

`.Branch` and `.Tag` replace every character that can't appear in an image tag with `-`, so the branch `feature/login` becomes `feature-login`.
Every rendered tag must match the OCI tag grammar, `[A-Za-z0-9_][A-Za-z0-9._-]{0,127}`, or the build fails before it starts.
A template that uses an unknown field also fails.
Duplicate tags are pushed once.

When `push.tags` is empty, the default tag depends on the source revision:

- with a branch: `{{ .Branch }}-{{ .ShortSHA }}-{{ .Timestamp }}`;
- with a tag: `{{ .Tag }}-{{ .ShortSHA }}-{{ .Timestamp }}`;
- with only a commit: `{{ .ShortSHA }}-{{ .Timestamp }}`;
- without a source: `build-{{ .Timestamp }}`.

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
- `userNamespaces`: sets `hostUsers: false` on the build pod when true.
  When unset, the controller's `--default-user-namespaces` flag decides, and that flag defaults to true.
  See [User namespaces](#user-namespaces).

### Concurrency

`concurrencyPolicy` controls what happens when a new build is needed while one is running.

- `Replace` (default): cancel the running build and start the new one.
  The controller deletes the running Job, and Kubernetes deletes its pods.
- `Wait`: let the running build finish, then build the latest state and skip any revisions in between.
  While it waits, `currentBuild` names the running Job and the `Reconciling` condition has reason `WaitingForBuild`.

Anything that changes the [build key](#build-key) counts as a new build, including a `requestedAt` annotation and the start of a new `rebuildInterval` period.

### Validation

The API server rejects invalid objects with CEL rules in the CRD:

- `dockerfile` must set exactly one of `path` or `inline`.
- `sourceRef` is required when `dockerfile.path` is set, `sourceRef.kind` must be `GitRepository`, and `sourceRef.name` must not be empty.
- `dockerfile.path` and `context` must be relative and must not contain `..`.
- `push.image` must not include a tag or digest, and `push.secretRef.name` must not be empty.
- `concurrencyPolicy` must be `Replace` or `Wait`.
- `interval` and `rebuildInterval` must be Go durations, and the Job limits must not be negative.

The schema sets every default in the example except `push.tags`.
The controller applies the default tags, because they depend on whether the build has a source.

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
`latestImage` is `<image>@<digest>` from the last successful build, or `<image>:<first tag>` if the build reported no digest.
`kubectl get imagebuilds` shows Ready, Revision, Latest Image, and Age columns.

### Conditions

| State | `Ready` | `Reconciling` | `Stalled` | Retry |
|---|---|---|---|---|
| Source missing or without an artifact | `False`, `SourceNotReady` | `True`, `ProgressingWithRetry` | | After `spec.interval` |
| Cross-namespace source with `--no-cross-namespace-refs` | `False`, `AccessDenied` | | `True`, `AccessDenied` | After a spec change |
| `push.tags` can't be rendered | `False`, `InvalidTags` | | `True`, `InvalidTags` | After a spec change |
| A build for the current key is running | `Unknown`, `Building` | `True`, `Building` | | When the Job or its pods change |
| An older build runs with `concurrencyPolicy: Wait` | `Unknown`, `WaitingForBuild` | `True`, `WaitingForBuild` | | When the Job or its pods change |
| The last build for the current key succeeded | `True`, `Succeeded` | | | After `spec.interval` or at the next `rebuildInterval` period |
| The last build for the current key failed | `False`, `BuildFailed` | | `True`, `BuildFailed` | After a key change |

A finished build records an Event with reason `Succeeded` or `BuildFailed`.
Other changes to `Ready` record an Event with the new reason, as a `Warning` when `Ready` is `False`.

## Build lifecycle

### Build key

The controller computes a build key as the SHA-256 hash of these inputs, encoded as JSON with sorted map keys:

- the source artifact digest;
- `dockerfile.path` or `dockerfile.inline`;
- `context`, only when the build has a source;
- `buildArgs`;
- `target`;
- `push.image`, the `push.tags` templates, `push.secretRef.name`, and `push.insecure`;
- the `reconcile.fluxcd.io/requestedAt` value;
- the current `rebuildInterval` period, `floor(now / rebuildInterval)`, if set.

The key leaves out settings that only change how a build runs: `cache`, `userNamespaces`, `resources`, `nodeSelector`, `tolerations`, `backoffLimit`, the history limits, `concurrencyPolicy`, `interval`, and `suspend`.
After changing one of those to fix a failed build, set the `requestedAt` annotation to retry it.

The key uses the artifact digest instead of the revision, so a commit that doesn't change the artifact doesn't rebuild.
The inputs include a version number, which changes whenever the list of inputs changes, so each `ImageBuild` rebuilds once after such an upgrade.

The controller starts a build only when the build key differs from the last recorded one.
Controller restarts, resyncs, and changes to `interval` or `suspend` never trigger rebuilds.
The controller does not check whether the image still exists in the registry.

### Triggers

- The referenced `GitRepository` publishes a new artifact.
- A spec field that affects the image changes.
- The `reconcile.fluxcd.io/requestedAt` annotation changes.
- `rebuildInterval` elapses, so the image picks up base image security patches.
  After a build, the controller checks again one second after the current period ends, or after `spec.interval` if that comes first.

If the source doesn't exist or has no artifact yet, the `ImageBuild` reports `Ready=False` with reason `SourceNotReady` and waits.
Like the Flux controllers, the controller builds from the source's last artifact even when its latest fetch failed.

### Build Job

Each build is a Job in the `ImageBuild` namespace, with a controller owner reference to the `ImageBuild`.
The Job name is `<name>-<first 10 characters of the build key>`.
The `ImageBuild` name is shortened so the whole Job name fits in 63 characters, because the Job controller uses the name as a label value, and dots become hyphens so pod hostnames stay valid.
After a restart, the controller finds and adopts a running Job for the current build key instead of creating a duplicate.

The controller records a finished Job's result only when `status.currentBuild` names that Job.
Every other finished Job is history.
If the build key returns to the key of a Job still in history, the controller deletes that Job and builds again, so the new build gets new tags.
A controller that crashes after creating a Job but before recording it in `currentBuild` adopts the Job if it's still running, and otherwise builds the key again.

The Job and its pods carry the `app.kubernetes.io/managed-by: image-build-controller` label.
The Job also carries these annotations:

- `build.fluxcd.leightweight.io/build-key`: the full build key;
- `build.fluxcd.leightweight.io/source-revision`: the source revision, for builds with a source;
- `build.fluxcd.leightweight.io/tags`: the comma-separated tags the build pushes;
- `build.fluxcd.leightweight.io/image`: the image name the build pushes to, so the result names the right image even if `push.image` changed during the build.

The pod has up to two containers:

1. For builds with a source, an init container that runs the controller image as `manager fetch --url <url> --digest <digest> --dest /workspace/context`.
   It uses `fluxcd/pkg/http/fetch` to download the Flux source artifact, verify its digest, and extract it into an `emptyDir` volume.
   It retries a failed download 9 times by default, like the Flux controllers, and skips symlinks in the artifact.
   It runs with the default seccomp profile, a read-only root filesystem, no capabilities, and an `emptyDir` at `/tmp`.
2. A build container that runs rootless BuildKit in daemonless mode with `buildctl-daemonless.sh`.
   It builds from the extracted context and pushes to the registry.

Builds without a source skip the init container and build from an empty `emptyDir`.
An inline Dockerfile reaches the build container through the downward API: the pod template has the Dockerfile in its `build.fluxcd.leightweight.io/dockerfile` annotation, and a `downwardAPI` volume turns the annotation into a file.
That needs no ConfigMap and no extra controller permissions.

The build container runs:

```sh
buildctl build --frontend dockerfile.v0 \
  --local context=/workspace/context/<context> --local dockerfile=<dir> --opt filename=<file> \
  [--opt target=<target>] [--opt build-arg:<name>=<value> ...] \
  --output 'type=image,"name=<image>:<tag>,<image>:<tag>",push=true[,registry.insecure=true]' \
  [--export-cache type=registry,ref=<image>:buildcache[,registry.insecure=true],mode=max \
   --import-cache type=registry,ref=<image>:buildcache[,registry.insecure=true]]
```

Build args are sorted by name, so the same `ImageBuild` always gives the same arguments.
With `push.secretRef`, the Secret's `.dockerconfigjson` key is mounted as `config.json` in the directory that `DOCKER_CONFIG` points to.

A controller flag sets the BuildKit image, and each controller release pins a default.

### Image digest

The build container reports the pushed image's digest through its termination message, so the controller doesn't need to read logs or query the registry.
A short `sh` script runs `buildctl` with `--metadata-file /workspace/output/metadata.json`, copies `containerimage.digest` out of that file, and writes it to `/dev/termination-log`.
`buildctl` can't write the metadata file to `/dev/termination-log` directly, because it writes with a temporary file and a rename.

On success, the termination message is exactly the digest, such as `sha256:...`.
On failure, the script writes nothing, so `FallbackToLogsOnError` makes Kubernetes use the tail of the build log instead.
The container's exit code tells the two apart.

### Failure handling

- The Job `backoffLimit` (default 2) retries transient failures such as registry timeouts.
- After the final attempt fails, the `ImageBuild` reports `Ready=False` and `Stalled=True` with reason `BuildFailed`.
- The controller never retries a failed build key on its own.
  A new revision, a spec change, or `requestedAt` starts a new build.
- The build and fetch containers use `terminationMessagePolicy: FallbackToLogsOnError`.
  Kubernetes copies the tail of the log into the termination message, and the controller puts it in the condition message and a Kubernetes Event without needing permission to read logs.
- The message comes from the attempt that finished last.
  If no pod left a message, the controller uses the reason and message of the Job's `Failed` condition.
- The API server limits an Event's note to 1024 bytes, so the Event keeps the end of a longer message, where the error is.
  The condition keeps the whole message.

### Build pod finalizer

Some clusters delete finished pods almost immediately.
With `--terminated-pod-gc-threshold=1` on kube-controller-manager, a finished build pod disappears within one 20-second garbage collection cycle, often before the controller reads its termination message.

The Job's pod template carries the `build.fluxcd.leightweight.io/termination-message` finalizer, so Kubernetes can't delete a build pod until the controller releases it:

1. The controller watches build pods through their `app.kubernetes.io/managed-by: image-build-controller` label.
2. When a build pod terminates, the controller copies its termination message into the `ImageBuild` status, the condition message, and an Event.
   The message is the [image digest](#image-digest) after a successful build and the tail of the build log after a failed one.
3. The controller then removes the finalizer.

The controller persists the status before it removes the finalizer, so a failed status update can't lose a termination message.

A second controller in the same process releases pods that nobody will read: build pods being deleted whose Job or `ImageBuild` is gone or being deleted.
It covers cancelled builds, history cleanup, and `ImageBuild` deletion, so they don't leave pods stuck.
If the controller is uninstalled while build pods exist, they stay until someone removes the finalizer, so the uninstall docs must cover it.

### History

Kubernetes deletes a build's Jobs through owner references when the `ImageBuild` is deleted.
`successfulBuildsHistoryLimit` (default 1) and `failedBuildsHistoryLimit` (default 3) limit how many finished Jobs the controller keeps, the same way `CronJob` does.
After recording a result, the controller deletes the oldest finished Jobs beyond the limits.
It never deletes a running Job or one whose result it hasn't recorded yet.
A limit of 0 deletes each finished Job once its result is recorded.

## Security

### Build pods

Rootless BuildKit needs these settings in every build pod:

- seccomp `Unconfined`, because the default profile blocks rootlesskit from creating its namespaces;
- AppArmor `Unconfined` on nodes that run AppArmor, because the default profile blocks the mounts BuildKit needs.

The baseline Pod Security Standard rejects both, so namespaces running builds must allow the `privileged` level.
The install docs must state this requirement.

Each `RUN` step normally gets its own process namespace, which needs a new `/proc` mount.
Kubernetes masks parts of a container's `/proc`, and the kernel refuses that mount from inside a masked `/proc`.
The build pod handles this differently with and without [user namespaces](#user-namespaces):

- With user namespaces, the container sets `procMount: Unmasked`, and `RUN` steps keep their own process namespace.
  Kubernetes only allows `procMount: Unmasked` together with `hostUsers: false`.
- Without user namespaces, BuildKit runs with `--oci-worker-no-process-sandbox`, and `RUN` steps share the BuildKit daemon's process namespace.
  A `RUN` step can then see and signal the daemon, which is weaker isolation.

Build pods also have these restrictions:

- run as non-root (UID 1000);
- no service account token mounted.

A read-only root filesystem is not yet tested.
See [Open risks](#open-risks).

### User namespaces

`userNamespaces: true` sets `hostUsers: false`, which maps container users to unprivileged host UIDs.
If a build escapes its container, it lands on the node as an unprivileged user.
It does not remove the `privileged` Pod Security requirement.
It requires Kubernetes 1.33 or later, Linux 6.3 or later, and containerd 2.0 or later or CRI-O on the nodes.
It is on by default, and the `--default-user-namespaces` controller flag changes the default.
The install docs must call out the default and the node requirements.

A user-namespaced pod has exactly 65536 user IDs.
The rootless BuildKit image's `/etc/subuid` and `/etc/subgid` start at 100000, outside that range, so rootlesskit fails to map its IDs.
The pod template carries replacements containing `user:1001:64535` in its `build.fluxcd.leightweight.io/subids` annotation.
A `downwardAPI` volume turns the annotation into `subuid` and `subgid` files, and the build container mounts them over `/etc/subuid` and `/etc/subgid` with `subPath`.
That works the same for builds with and without a source, so no init container is needed for it.

This setup built an image successfully on kind and on Talos 1.13 with Kubernetes 1.36, Linux 6.18, and containerd 2.2.
The rendered build Jobs also built and pushed images on kind with Kubernetes 1.37, with and without user namespaces, with and without a source.

### Network access to source-controller

By default, `flux bootstrap` installs NetworkPolicies that only allow pods in `flux-system` to reach source-controller.
Build pods in other namespaces cannot download artifacts without an extra policy.
The Helm chart includes an optional NetworkPolicy that allows traffic to the source-controller artifact port from pods labelled `app.kubernetes.io/managed-by: image-build-controller` in any namespace.
`sourceControllerNetworkPolicy.enabled` turns it on, and it is off by default, because the chart would otherwise create an object in another release's namespace.
The example install values enable it with a comment that explains why, because most Flux installs add the restrictive policies, including installs that don't use the Flux CLI.

### Controller RBAC

The controller watches `ImageBuild` resources across the whole cluster with these permissions:

- `ImageBuild`: get, list, watch, update, patch, and update status;
- `ImageBuild` finalizers: update, because clusters that enforce owner reference permissions require it to create Jobs that block their `ImageBuild`'s deletion;
- `Job`: create, delete, get, list, and watch;
- `Pod`: get, list, watch, and patch, to read termination messages and remove the [build pod finalizer](#build-pod-finalizer);
- `GitRepository`: get, list, and watch;
- `Event`: create and patch.

The controller never reads Secrets.
Build pods mount the registry Secret directly.

### Controller flags

- `--buildkit-image`, the BuildKit image, defaulting to the image the release pins.
- `--fetch-image`, the image for the fetch init container.
  It defaults to the controller's own image, which the controller reads from its pod at startup through the `POD_NAMESPACE` and `POD_NAME` environment variables that the Deployment sets with the downward API.
  That needs no configuration, and the init container always runs the same controller version.
- `--default-user-namespaces`, the default for `userNamespaces`, which is true.
- `--no-cross-namespace-refs`, which rejects `sourceRef`s in other namespaces, as Flux does.

## Implementation

- Project skeleton: kubebuilder, for controller-runtime, CRD and RBAC generation, and envtest.
- Flux runtime: `fluxcd/pkg/runtime` for conditions, patch helpers, event recording, and `requestedAt` handling.
- Source types: `fluxcd/source-controller/api`.
- Artifact fetching: `fluxcd/pkg/http/fetch`.
- Caching: the manager caches only Jobs and pods with the `app.kubernetes.io/managed-by: image-build-controller` label, so it doesn't hold every Job and pod in the cluster in memory.
- Build backend: rootless BuildKit behind a small internal interface.
  The API exposes only BuildKit.

## Distribution

- Helm chart: the main documented install method, published as an OCI artifact at `oci://ghcr.io/leightweight/charts/image-build-controller`.
- `install.yaml`: a release asset generated from the kubebuilder Kustomize config, available but not the main documented method.
- Controller images: `ghcr.io/leightweight/image-build-controller`, for `linux/amd64` and `linux/arm64`, built and published by goreleaser.

### Releases

A maintainer releases by pushing a `v*` tag, such as `v0.1.0`.
The tag triggers `.github/workflows/release.yml`, which:

1. runs goreleaser, which cross-compiles the manager, builds and pushes the multi-arch image with an SBOM, signs it, and creates the GitHub release with `install.yaml` and `checksums.txt`;
2. packages the Helm chart with the tag's version as both the chart version and `appVersion`, pushes it, and signs it.

The chart version always matches the controller version.
The workflow signs the image and the chart with cosign and no keys, using its GitHub Actions OIDC identity.
It uses cosign v3's default Sigstore bundle format, because v3 deprecates the older format.
The release notes are GitHub's generated notes, which list the merged PRs.

`Dockerfile.release` builds the release image from goreleaser's prebuilt binaries, so it needs no emulation for other platforms.
The root `Dockerfile` builds from source for development and the end-to-end tests.
`mise run release:snapshot` builds the release locally without publishing it, and the Lint job runs `goreleaser check`.

### Helm chart

Kubebuilder's `helm/v2-alpha` plugin generates the chart in `charts/chart` from the `config/default` Kustomize output.
`mise run chart` regenerates it, and CI fails when the committed chart doesn't match `config/`.
The plugin regenerates every template that comes from Kustomize.
It keeps `Chart.yaml`, `values.yaml`, and templates it didn't create, so hand edits go only in those files and in `templates/extras/`.

The chart's defaults:

- The controller runs one replica with leader election.
- The metrics endpoint serves HTTPS with controller-runtime's self-signed certificate and Kubernetes authentication.
- cert-manager integration is off, because the controller has no webhooks.
- The Prometheus `ServiceMonitor` is off, because it needs the prometheus-operator CRDs.
- The NetworkPolicy for the metrics endpoint and the RBAC helper roles are off, as the plugin generates them.
- The source-controller NetworkPolicy is off.
- The CRDs are templates with Helm's `keep` resource policy, so upgrades update them and uninstalls keep them.

Controller flags go in the `manager.args` list, because typed values would need edits to the generated Deployment template.

## CI

GitHub Actions runs:

- golangci-lint;
- unit and controller tests with envtest;
- an end-to-end test on kind that builds a real image with Flux and a local registry.

The end-to-end test installs Flux source-controller, a Gitea server, and a plain HTTP registry in the kind cluster.
It installs the controller with the Helm chart and the example values in `test/e2e/testdata/values.yaml`.
It commits a Dockerfile to a Gitea repository and points a `GitRepository` at it.
The example values enable the chart's source-controller NetworkPolicy, and the test keeps Flux's default policies, so build pods download artifacts through it.
On hosts with DMI data, such as GitHub's runners, kind bind-mounts files over `/sys/devices/virtual/dmi/id` in each node.
The kernel refuses new sysfs mounts in user namespaces while part of `/sys` is hidden, so `mise run test:e2e` removes those mounts after it creates the cluster.
Its specs check that:

- a `dockerfile.path` build pushes the default branch tag, and the registry serves the digest in `status.latestImage`;
- a new commit triggers a rebuild with the new commit's tag;
- an inline Dockerfile builds without a source and gets the `build-<timestamp>` tag;
- a failed build sets `Ready` to `False` with the `BuildFailed` reason and the end of the build log in its message.

## Deferred

- Flux notifications through notification-controller.
  The author already uses Flux notifications, so this comes first after v1.
  The `Alert` API may restrict event source kinds, so this needs verification.
  For v1, the controller emits standard Kubernetes Events only.
  It needs a controller flag for the notification-controller events address.
- A controller flag that restricts the controller to a single namespace.
- Build secrets (`--secret`).
- Multi-arch image builds.
- Additional build backends.
- Rebuilding when a base image digest changes.
- Per-`ImageBuild` BuildKit image overrides.
- Cloud workload identity for registry authentication.
- `OCIRepository` and `Bucket` sources.

## Open risks

1. Whether notification-controller accepts events from custom kinds.
2. Whether rootless BuildKit works with a read-only root filesystem, and which paths need writable volumes.
   The `fetch` init container also needs a writable `$TMPDIR`, because the Flux fetcher downloads to a temporary file before extracting it.

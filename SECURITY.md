# Security policy

## Reporting a vulnerability

Report vulnerabilities through GitHub's [private vulnerability reporting](https://github.com/leightweight/image-build-controller/security/advisories/new), not in public issues.
Include the version, how to reproduce the problem, and its impact.

## Supported versions

Only the latest release gets security fixes.

## Security model

- The controller never reads Secrets, and its RBAC doesn't allow it to.
  Build pods mount registry credentials directly.
- Build pods run rootless BuildKit as a non-root user, without a service account token.
- By default, build pods run in a user namespace, so a build that escapes its container lands on the node as an unprivileged user.
- Rootless BuildKit needs the `Unconfined` seccomp and AppArmor profiles, so namespaces that run builds must allow the `privileged` Pod Security level.
  Keep builds in namespaces you trust with that level.
- A build runs the Dockerfile from its source, so anyone who can push to the Git repository or edit the `ImageBuild` can run code in the build pod and push images with its registry credentials.
- With `--no-cross-namespace-refs`, the controller rejects `ImageBuild` resources that reference a source in another namespace.
- The release workflow signs the controller image and the Helm chart with cosign.
  The [README](README.md#verify-the-release) shows how to verify them.

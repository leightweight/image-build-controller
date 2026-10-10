---
paths:
  - "internal/**/*.go"
  - "cmd/**/*.go"
---

# Controller

- Do not assume a source exists.
  Inline-Dockerfile builds have no `sourceRef`, so values derived from the revision, such as the branch and SHA, can be empty.
- Do not keep build state only in controller memory.
  The controller must be able to resume any build from the `ImageBuild` status and its Jobs after a restart.

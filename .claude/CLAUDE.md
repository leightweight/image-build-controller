# image-build-controller

A Kubernetes controller that builds container images from Flux sources so Flux image automation can deploy them.

[`.claude/docs/design.md`](docs/design.md) is the source of truth for the API, build lifecycle, and security model.

Project instructions live in [`.claude/rules/`](rules/), one topic per file.
Rules with `paths` frontmatter load only when working on matching files.

Task-specific procedures will live as skills in `.claude/skills/`.
When a rule points to a skill, load it before doing that task.

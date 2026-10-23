# Commits and pull requests

## Conventional Commits (required)

PR titles must follow Conventional Commits.
Intermediate commit subjects should too, even though squash merges replace them with the PR title.

Format: `<type>[optional scope][optional !]: <description>`

Types:

- `feat`: new user-visible behavior.
- `fix`: bug fixes to controller or build behavior (not CI, docs, or tooling).
- `refactor`: code changes with no behavior change.
- `perf`: performance improvements.
- `test`: test-only changes.
- `docs`: documentation changes.
- `ci`: CI and automation changes.
- `chore`: maintenance, releases, dependency updates, and tooling.
- `security`: security-related changes.
- `revert`: reverting a previous change.

Scopes:

- `api`: `ImageBuild` types and CRDs.
- `controller`: reconciliation logic.
- `build`: build Job and BuildKit integration.
- `fetch`: the artifact fetch subcommand.
- `chart`: the Helm chart.
- `deps`: dependency updates.

Description style:

- Start with a lowercase word or an acronym such as `CRD` or `RBAC`.
- Use imperative mood ("add", not "added").
- Keep it concise but descriptive.

Examples:

- `feat(api): add rebuildInterval to ImageBuild`
- `fix(controller): adopt existing Job after restart`
- `chore(deps): bump fluxcd/pkg/runtime`

## PR titles and descriptions are release notes

Release notes come from PR titles and descriptions, so write them for a user who hasn't read the diff.
Lead with the user-visible problem and outcome.
Show a concrete example, such as YAML or `kubectl` output, when it helps.
Keep the description in sync with the final diff.
Write descriptions in normal paragraphs, with each paragraph on one line.
GitHub shows a single newline in a description as a line break, so the one-sentence-per-line rule for Markdown files doesn't apply.
The same goes for issues and comments.

## Pre-commit

`.config/hk.pkl` defines a git `pre-commit` hook that runs the linters with auto-fix.
Its `stash = "git"` setting stashes unstaged changes while the hook runs, so fixes only touch staged content.
Run `mise run lint-fix` and stage any fixes before committing.

# Commands

Run everything through mise tasks.
If a task you need doesn't exist, add it to `.config/mise/config.toml` and list it in this file instead of running the command directly.

- `mise bootstrap`: set up a fresh checkout.
  It runs the `bootstrap` task, which installs the git hooks.
- Before `mise run` and `mise exec`, mise downloads Go module dependencies if they changed (`[deps.go]` with `auto = true`).
- `mise run lint`: run all linters.
- `mise run lint-fix` (alias `fix`): run all linters and apply fixes.
- `mise tasks ls`: list all available tasks.

`.config/mise/config.toml` pins the tool versions.
Linter configuration lives in `.config/`, in `hk.pkl` and `rumdl.toml`.

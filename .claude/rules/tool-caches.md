# Tool caches and binaries

Don't let tools create cache or output folders around the repo, such as `.rumdl_cache` or `__pycache__`.
Point every tool cache at `.cache/<tool>/` at the repo root.
Prefer an environment variable in the `[env]` section of `.config/mise/config.toml` that uses `{{config_root}}`, so the path is the same from any subdirectory.
Send generated reports and other build byproducts, such as coverage profiles, to `.cache/` too.
Tools whose caches already live outside the repo by default, such as the Go build and module caches, can keep them there.

Binaries go in `bin/` at the repo root.
The `_.path` setting in `.config/mise/config.toml` adds `bin/` to `PATH`, so tasks can call them by name.
Install tools through mise `[tools]` whenever a backend supports them.
Only put a binary in `bin/` when mise can't install it, such as the custom golangci-lint build, or when the project builds it.

`.cache/` and `bin/` are the only gitignored folders.
If a new tool can't use them, ask the user instead of adding another ignore entry.

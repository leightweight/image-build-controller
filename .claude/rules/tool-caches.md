# Tool caches and outputs

Don't let tools create cache or output folders around the repo, such as `.rumdl_cache`, `bin/`, or `__pycache__`.
Point every tool cache at `.cache/<tool>/` at the repo root.
Prefer an environment variable in the `[env]` section of `.config/mise/config.toml` that uses `{{config_root}}`, so the path is the same from any subdirectory.
`.cache/` is the only gitignored cache folder.
If a new tool can't write its cache there, ask the user instead of adding another ignore entry.
Tools whose caches already live outside the repo by default, such as the Go build and module caches, can keep them there.

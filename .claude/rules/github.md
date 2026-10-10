# GitHub interactions

- Open pull requests against `main` unless the user names another base branch.
- Never post to GitHub without the user reviewing the content first.
  This covers issues, discussions, PR comments, and reviews.
- Append this disclosure to any AI-contributed GitHub content, in the format `*AI-assisted. Tool: <tool>; model: <provider>/<model>; version: <tool-version-or-unavailable>.*`.
  The version is the tool's version, such as the output of `claude --version`.
  Use the exact identifiers the runtime exposes.
  Never guess them, and use `unavailable` when a value isn't exposed.

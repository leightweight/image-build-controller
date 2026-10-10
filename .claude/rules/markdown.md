---
paths:
  - "**/*.md"
---

# Markdown

Write one sentence per line, and never hard-wrap prose at a column width.
`rumdl` enforces this with MD013 in `sentence-per-line` mode, and `mise run lint-fix` reflows violations.
`rumdl` only starts a new sentence at a capitalized word.
Don't begin a sentence with a lowercase word such as `v1` or with a code span, or `rumdl` joins it to the previous line.
Rephrase the sentence instead.

---
title: {{printf "%q" .Title}}
created_at: {{printf "%q" .CreatedAt}}
updated_at: {{printf "%q" .UpdatedAt}}
---

# {{.Title}}

LOOM indexing reads these timestamps but never rewrites source frontmatter.
Update `updated_at` when the note changes materially.

# Documentation Writing

Read the README, current state, relevant existing docs, and implementation before
changing public claims. Explain the reader's goal and the responsible component
before commands. Check command syntax against the current CLI and label examples
that need an operator's own path, account, network or machine configuration.

Keep each page complete, with unique YAML `title`, `description`, `audience`,
lowercase `tags`, honest `status`, `source_scope` and `related` wikilinks.
Preserve existing titles/aliases when practical. A verified date records past
checks; it does not promise that a live installation is currently healthy.

Use the templates in `.project/templates/docs/` as authoring aids. Replace their
human-fill fields before adding a page to `docs/`. Preserve wikilinks for the
documentation graph and add normal Markdown links for GitHub navigation.

Never reference unpublished private receipts as required instructions. Clearly
separate implementation, configuration, observed acceptance and planned work.
Run `go test ./internal/loomdocs` and the documentation status command after
changing metadata or links. Those checks do not execute every documented action.

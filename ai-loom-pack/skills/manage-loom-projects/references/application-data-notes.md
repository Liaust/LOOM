# Application Data In Notes

Read files from an existing same-project managed application allocation:

```yaml
resources:
  papers:
    kind: knowledge
    knowledge:
      application_data: {application: webdav, data: library, subpath: papers}
      category: research
```

`webdav` is an existing application resource and `library` is its declared data
key with a managed `binding_ref`. Omit `subpath` for the entire allocation. Use
this instead of `path`, not alongside it. Apply through ordinary project
plan/apply/status; missing allocation is a prerequisite. Install the application
first. A source change needs a new plan, not manual watcher configuration.

The application retains write and backup ownership. Do not add knowledge
`protection`, use cross-project references, arbitrary host paths or symlinks.
Normal knowledge refresh/processing settings apply. The host must permit the
node agent to read private application files; missing access needs an operator,
not a chmod workaround.

Withdrawal or project archive stops intake and current search/read access;
retained versions are not deleted. Project-folder archives do not move external
data or create archive custody for it. Re-enrollment requires current owner
evidence. This does not implement a sync client or accept anything into Provenance.

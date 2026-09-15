# LOOM-Installed Skills

This is the LOOM-managed external skill set, exposed through Hermes'
`skills.external_dirs`. The scaffold provides only this read-only instruction;
it does not install skills or register ownership state.

Hermes' native/bundled and created skills remain in `.hermes/skills` with pilot
write approval. Follow `protocols/SKILL-CREATION-AND-PROMOTION.md` from the
workspace root. Installed copies and their protected parents are read-only to
the agent. Promotion and deployment require the reviewed repository workflow
and operator-controlled ownership; do not redirect either store with symlinks.

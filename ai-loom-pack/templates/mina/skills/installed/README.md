# LOOM-Installed Skills

This retains the historical LOOM-managed Hermes skill deployment. Active
Codex skills use the supported account/admin native roots, not this catalogue.
The scaffold provides only this read-only instruction;
it does not install skills or register ownership state.

Historical Hermes skills remain preserved in `.hermes/skills`. New MINA skills
use workspace `.agents/skills` without a per-write approval queue.
Follow `protocols/SKILL-CREATION-AND-PROMOTION.md` from the
workspace root. Installed copies and their protected parents are read-only to
the agent. Promotion and deployment require the reviewed repository workflow
and operator-controlled ownership; do not redirect either store with symlinks.

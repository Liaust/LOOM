# Keep mutable checkout metadata and acceptance output out of Nix source copies.
{ root ? ../.. }:
let
  rootPath = toString root;
  source =
    if builtins.pathExists (rootPath + "/.git") then
      builtins.flakeRefToString { type = "git"; url = rootPath; }
    else
      # Exported validation trees have no Git index. Preserve their sources,
      # excluding only the repository's metadata and declared scratch root.
      builtins.unsafeDiscardStringContext (toString (builtins.path {
        name = "source";
        path = root;
        filter = path: _: !(builtins.elem path [
          (rootPath + "/.git")
          (rootPath + "/.loom-acceptance")
        ]);
      }));
in
builtins.getFlake source

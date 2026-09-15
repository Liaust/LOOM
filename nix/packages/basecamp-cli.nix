{ basecamp-upstream, system }:

let
  revision = "e4bfd014bf137771d454515b4f7314ce651ee096";
  upstream = basecamp-upstream.packages.${system}.basecamp;
in
assert basecamp-upstream.rev == revision;
assert upstream.version == "0.10.0";
assert upstream.vendorHash == "sha256-zNTp8pw3ZViwSmpsxQ78MU5lLgn0P2nb9aAxqp/96Eg=";
upstream.overrideAttrs (old: {
  # Retain the official source, dependency hash and build recipe. Stamp only
  # immutable release metadata; never import a host profile or auth state.
  ldflags = old.ldflags ++ [
    "-X github.com/basecamp/basecamp-cli/internal/version.Commit=${revision}"
  ];
})

{ lib, sqlite, fetchurl }:

if lib.versionAtLeast sqlite.version "3.51.3" then sqlite else
sqlite.overrideAttrs (old: {
  version = "3.51.3";
  src = fetchurl {
    url = "https://sqlite.org/2026/sqlite-src-3510300.zip";
    hash = "sha256-+KZ6H1tcrnxtQvCZTKe/GkpYWIaMgq3J/BNAvtXrjNI=";
  };
  docsrc = fetchurl {
    url = "https://sqlite.org/2026/sqlite-doc-3510300.zip";
    hash = "sha256-7AU+ZqM7BHm0tMiBM67vTQIhTWPlotWe0GLK9QYeoiw=";
  };
  # Upstream's 3.51 test cleanup; keep the WAL regression and build checks enabled.
  # https://sqlite.org/forum/forumpost/b4b47751e8
  patches = (old.patches or [ ]) ++ [ ./hermes-sqlite-test-cleanup.patch ];
  postInstall = ''
    mkdir -p $doc/share/doc
    unzip $docsrc
    mv sqlite-doc-3510300 $doc/share/doc/sqlite
  '';
})

# Artifact producer metadata only. E3 realizes/delivers the closure and publishes
# a descriptor with source/NAR/schema digests through separate publisher authority.
{ lib }:
{ package, repositoryID, sourceRevision, sourceDigest, artifactRef, artifactDigest
, configuration, dataFormat, compatibleDataFormats ? [], executable ? "${package}/bin/loom-application-launcher"
}:
assert lib.hasPrefix "repo_" repositoryID;
assert builtins.elem package.system [ "x86_64-linux" "aarch64-linux" ];
{
  schema_version = "application.artifact.v1";
  repository_id = repositoryID;
  source_revision = sourceRevision;
  source_digest = sourceDigest;
  artifact = { ref = artifactRef; digest = artifactDigest; platform = package.system; };
  store_root = toString package;
  launcher = "${package}/bin/loom-application-launcher";
  inherit executable configuration;
  data_format = dataFormat;
  compatible_data_formats = compatibleDataFormats;
  # Absence is intentional: NAR and complete closure digests must come from
  # realized content, never a build plan or an asserted reviewed boolean.
}

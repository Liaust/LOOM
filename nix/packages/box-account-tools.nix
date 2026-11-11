{ lib, runCommand, bash, python3, basecamp-cli, gh, git, loom }:
runCommand "loom-box-account-tools" { } ''
  mkdir -p "$out/bin" "$out/lib"
  cp ${../files/hermes-platform/box_tools.py} "$out/lib/box_tools.py"
  cp ${../files/hermes-platform/company_tools.py} "$out/lib/company_tools.py"
  cp ${../files/hermes-platform/fixed_basecamp.py} "$out/lib/fixed_basecamp.py"
  cat > "$out/bin/loom-box-tool" <<EOF
#!${bash}/bin/bash
exec ${python3}/bin/python3 -B "$out/lib/box_tools.py" \
  --loom ${loom}/bin/loom --basecamp ${basecamp-cli}/bin/basecamp \
  --gh ${gh}/bin/gh --git ${git}/bin/git --contexts /etc/loom-codex-box-contexts.json "\$@"
EOF
  for tool in basecamp gh; do
    cat > "$out/bin/$tool" <<EOF
#!${bash}/bin/bash
exec "$out/bin/loom-box-tool" $tool "\$@"
EOF
  done
  chmod 0555 "$out/bin/"*
''

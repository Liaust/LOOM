#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

root="${LOOM_ACCEPTANCE_ROOT:-$repo_root/.loom-acceptance/v0.6.8-filesystem-fidelity}"
rm -rf "$root"
mkdir -p "$root"

cleanup() {
  chmod -R u+rwX "$root" 2>/dev/null || true
}
trap cleanup EXIT

mkdir -p \
  "$root/zero-byte" \
  "$root/empty-dir/empty" \
  "$root/executable" \
  "$root/symlink" \
  "$root/broken-symlink" \
  "$root/hardlink" \
  "$root/sparse" \
  "$root/package-dir/Example.app/Contents" \
  "$root/apple-metadata" \
  "$root/unicode" \
  "$root/permission-denied" \
  "$root/large-text" \
  "$root/git-repo" \
  "$root/live-mutation"

: > "$root/zero-byte/empty.txt"
printf '#!/usr/bin/env bash\nprintf loom\\n\n' > "$root/executable/run.sh"
chmod +x "$root/executable/run.sh"
printf 'target\n' > "$root/symlink/target.txt"
ln -s "target.txt" "$root/symlink/link-to-target" 2>/dev/null || true
ln -s "missing.txt" "$root/broken-symlink/broken" 2>/dev/null || true
printf 'linked\n' > "$root/hardlink/source.txt"
ln "$root/hardlink/source.txt" "$root/hardlink/also-source.txt" 2>/dev/null || true
dd if=/dev/zero of="$root/sparse/sparse.bin" bs=1 count=0 seek=1048576 2>/dev/null || true
printf 'package marker\n' > "$root/package-dir/Example.app/Contents/Info.plist"
printf 'apple metadata sidecar\n' > "$root/apple-metadata/._sidecar"
printf 'unicode\n' > "$root/unicode/café.md"
printf 'secret\n' > "$root/permission-denied/secret.txt"
chmod 000 "$root/permission-denied/secret.txt" 2>/dev/null || true
for i in $(seq 1 2000); do
  printf 'large text line %04d\n' "$i"
done > "$root/large-text/large.txt"
git -C "$root/git-repo" init -q
printf 'repo\n' > "$root/git-repo/README.md"
printf 'before\n' > "$root/live-mutation/changing.txt"
printf 'after\n' >> "$root/live-mutation/changing.txt"

if command -v xattr >/dev/null 2>&1; then
  printf 'xattr\n' > "$root/apple-metadata/commented.txt"
  xattr -w com.apple.metadata:kMDItemFinderComment "LOOM acceptance" "$root/apple-metadata/commented.txt" 2>/dev/null || true
fi

echo "Created filesystem fidelity fixtures under $root"
go test ./internal/filesystemmeta ./internal/storagefidelity ./internal/storageexport ./internal/storageretention
go run ./cmd/loom storage fidelity backfill --help >/dev/null
echo "v0.6.8 local filesystem fidelity smoke passed"

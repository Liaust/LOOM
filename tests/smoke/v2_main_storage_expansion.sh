#!/usr/bin/env bash
set -euo pipefail

# The manifest mode is read-only. The smoke mode accepts no real block device.
manifest() {
  python3 - "$1" <<'PY'
import os, sys, stat, json, hashlib, base64
root = os.path.abspath(os.fsencode(sys.argv[1]))
root_stat = os.lstat(root)
if not stat.S_ISDIR(root_stat.st_mode):
    raise SystemExit('root must be a real directory')
device = root_stat.st_dev
stack = [(b'.', root)]
rows = []
links = {}
while stack:
    rel, path = stack.pop()
    before = os.lstat(path)
    if before.st_dev != device:
        raise SystemExit('unexpected filesystem boundary')
    kind = stat.S_IFMT(before.st_mode)
    if kind not in (stat.S_IFDIR, stat.S_IFREG, stat.S_IFLNK):
        raise SystemExit('special entry refused: ' + repr(rel))
    row = dict(path=base64.b64encode(rel).decode(), type=kind,
               mode=stat.S_IMODE(before.st_mode), uid=before.st_uid,
               gid=before.st_gid, mtime_ns=before.st_mtime_ns)
    row['xattrs'] = sorted((name, hashlib.sha256(os.getxattr(
        path, name, follow_symlinks=False)).hexdigest())
        for name in os.listxattr(path, follow_symlinks=False))
    if kind == stat.S_IFDIR:
        fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_NOATIME)
        try:
            with os.scandir(fd) as entries:
                for entry in entries:
                    name = os.fsencode(entry.name)
                    stack.append((name if rel == b'.' else rel + b'/' + name, path + b'/' + name))
        finally:
            os.close(fd)
    elif kind == stat.S_IFLNK:
        row['target'] = base64.b64encode(os.readlink(path)).decode()
    else:
        row['size'] = before.st_size
        links.setdefault(before.st_ino, []).append((rel, before.st_nlink))
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NOATIME)
        try:
            held = os.fstat(fd)
            if (held.st_dev, held.st_ino) != (before.st_dev, before.st_ino):
                raise SystemExit('file replaced before hashing')
            digest = hashlib.sha256()
            while True:
                chunk = os.read(fd, 1024 * 1024)
                if not chunk: break
                digest.update(chunk)
            row['sha256'] = digest.hexdigest()
        finally:
            os.close(fd)
    after = os.lstat(path)
    def stable(s):
        return (s.st_dev, s.st_ino, s.st_mode, s.st_uid, s.st_gid,
                s.st_size, s.st_mtime_ns, s.st_ctime_ns, s.st_nlink)
    if stable(before) != stable(after):
        raise SystemExit('entry changed during inventory: ' + repr(rel))
    rows.append((rel, row))
groups = {}
for group in links.values():
    if group[0][1] != len(group): raise SystemExit('hard link outside tree')
    if len(group) > 1:
        first = base64.b64encode(min(p for p, _ in group)).decode()
        groups.update((p, first) for p, _ in group)
for rel, row in sorted(rows):
    if rel in groups: row['hardlink_group'] = groups[rel]
    print(json.dumps(row, sort_keys=True, separators=(',', ':'), ensure_ascii=True))
PY
}

if [[ ${1:-} == manifest && $# == 2 ]]; then
  manifest "$2"
  exit
fi
if [[ $# != 0 || $(uname -s) != Linux || $EUID != 0 ]]; then
  echo 'Run smoke as Linux root with no arguments, or manifest <root> for read-only inventory.' >&2
  exit 2
fi
if [[ ${LOOM_STORAGE_SMOKE_PRIVATE_NS:-} != 1 ]]; then
  exec unshare --mount --propagation private env LOOM_STORAGE_SMOKE_PRIVATE_NS=1 bash "$0"
fi
umask 077
root=$(mktemp -d /tmp/loom-ssd-proof.XXXXXX)
loop=''
cleanup() {
  mountpoint -q "$root/target" && umount "$root/target"
  [[ -z $loop ]] || losetup -d "$loop"
  rm -rf -- "$root"
}
trap cleanup EXIT
mkdir "$root/source" "$root/target"
truncate -s 128M "$root/disk.img"
loop=$(losetup --find --show "$root/disk.img")
[[ $(losetup -n -O BACK-FILE "$loop") == "$root/disk.img" ]]
mkfs.ext4 -q -F "$loop"
mount -o noatime "$loop" "$root/target"
rmdir "$root/target/lost+found"
python3 - "$root/source" <<'PY'
import os, sys, struct
p=sys.argv[1]
os.mkdir(p+'/dir')
with open(p+'/dir/file', 'wb') as f: f.write(b'fixture\x00payload\n')
os.link(p+'/dir/file', p+'/hard link')
os.symlink('dir/file', p+'/symbolic link')
os.symlink('/outside-not-followed', p+'/absolute link')
with open(p+'/sparse', 'wb') as f: f.seek(8*1024*1024); f.write(b'end')
with open(p+'/unicode-\u03bb\nname', 'wb') as f: f.write(b'odd path')
os.chown(p+'/dir/file', 12345, 12346)
os.setxattr(p+'/dir/file', 'user.fidelity', b'xattr\x00bytes')
acl=struct.pack('<I',2)+b''.join(struct.pack('<HHI',tag,perm,uid) for tag,perm,uid in [
    (1,7,0xffffffff),(2,4,12347),(4,5,0xffffffff),(16,5,0xffffffff),(32,0,0xffffffff)])
os.setxattr(p+'/dir', 'system.posix_acl_access', acl)
os.setxattr(p+'/dir', 'system.posix_acl_default', acl)
for parent, dirs, files in os.walk(p, topdown=False):
    for name in dirs+files: os.utime(parent+'/'+name, ns=(1788000000123456789,1788000000987654321),follow_symlinks=False)
os.utime(p, ns=(1788000000123456789,1788000000987654321))
PY
manifest "$root/source" > "$root/before.jsonl"
rsync -aHAXSUx --numeric-ids --open-noatime --modify-window=-1 --no-devices --no-specials "$root/source/" "$root/target/"
manifest "$root/source" > "$root/source.jsonl"
manifest "$root/target" > "$root/target.jsonl"
cmp "$root/before.jsonl" "$root/source.jsonl"
cmp "$root/source.jsonl" "$root/target.jsonl"
[[ $(stat -c %b "$root/target/sparse") -lt 128 ]]
umount "$root/target"
mount -o noatime "$loop" "$root/target"
manifest "$root/target" > "$root/remounted.jsonl"
cmp "$root/source.jsonl" "$root/remounted.jsonl"
mkfifo "$root/target/refused-fifo"
if manifest "$root/target" > /dev/null 2>&1; then echo 'special entry was accepted' >&2; exit 1; fi
umount "$root/target"
manifest "$root/source" > "$root/rollback.jsonl"
cmp "$root/source.jsonl" "$root/rollback.jsonl"
echo 'PASS: copy, content/metadata/ACL/xattr/link/mtime fidelity, sparse allocation, remount, special refusal, retained-source rollback'

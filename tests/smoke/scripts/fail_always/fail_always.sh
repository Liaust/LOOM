#!/usr/bin/env sh
set -eu

echo "fail_always fixture starting"
echo "fail_always fixture intentional failure" >&2
exit 42

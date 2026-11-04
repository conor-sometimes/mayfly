#!/usr/bin/env bash
# The smoke matrix's only check: the subject binary installed onto PATH, and it
# runs and prints what it should. Exit 0 is a pass (the `test = ...` tier).
set -euo pipefail

if ! command -v hello >/dev/null 2>&1; then
  echo "hello is not on PATH after provisioning"
  exit 1
fi

out="$(hello)"
if [ "$out" != "mayfly hello" ]; then
  echo "hello printed: $out"
  exit 1
fi

echo "smoke ok: $out"

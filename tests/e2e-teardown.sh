#!/usr/bin/env bash
# e2e: a FAILING test still tears the VM down. Clean-exit teardown is exercised
# by every passing run (and the soak); this covers the other branch - the runner
# must kill the VM and remove the overlay even when the distro fails, not only on
# success. Needs KVM. Usage: tests/e2e-teardown.sh [path-to-mayfly-flake]
set -uo pipefail

repo="${1:-$PWD}"
work="$(mktemp -d)"
cleanup() { rm -rf "$work"; }
trap cleanup EXIT

cd "$work"
# A one-distro run whose test deliberately exits non-zero.
cat > mayfly.nix <<NIX
{ ... }:
{
  distros.ubuntu-2404.enable = true;
  test = builtins.toFile "fail.sh" "#!/bin/sh\necho 'deliberately failing'\nexit 1\n";
}
NIX

qcount() { pgrep -cf 'qemu-system' 2>/dev/null || true; }

before="$(qcount)"
nix run "$repo" > run.log 2>&1
rc=$?

if [ "$rc" -eq 0 ]; then
  echo "FAIL: a failing test must make the run exit non-zero (got 0)"
  tail -8 run.log
  exit 1
fi

# Give the teardown trap a moment, then assert no VM leaked.
sleep 3
after="$(qcount)"
if [ "$after" -gt "$before" ]; then
  echo "FAIL: leaked a qemu process (qemu count $before -> $after)"
  exit 1
fi

# The run's overlay/work dir lives under MAYFLY_RUN_DIR (mktemp'd per run) and is
# removed by the trap on exit; nothing of this run should remain in the run.log's
# reported work dir.
echo "teardown-on-failure OK: run exited $rc, qemu $before -> $after, no VM leaked"

#!/usr/bin/env bash
# mayfly caddy demo: the shipped Caddy binary serves on this distro. Runs as
# root in the guest; exit 0 is a pass.
set -uo pipefail

fail() {
  echo "CADDY CHECK FAIL: $*"
  systemctl status caddy.service --no-pager 2>/dev/null | tail -15 || true
  echo "---- journalctl -u caddy ----"
  journalctl -u caddy.service --no-pager 2>/dev/null | tail -25 || true
  exit 1
}

# The unit comes up and stays up.
for _ in $(seq 1 30); do
  [ "$(systemctl is-active caddy.service 2>/dev/null)" = active ] && break
  sleep 1
done
[ "$(systemctl is-active caddy.service 2>/dev/null)" = active ] || fail "caddy.service is not active"

# The shipped binary actually serves the page. The harness exempts loopback from
# the package-install proxy (no_proxy=localhost,127.0.0.1,::1), so a plain curl
# to 127.0.0.1 reaches caddy directly.
out=""
for _ in $(seq 1 30); do
  out="$(curl -fsS http://127.0.0.1:8080/ 2>/dev/null)" && break
  sleep 1
done
printf '%s' "$out" | grep -q "mayfly-caddy-ok" || fail "unexpected response: '${out}'"

. /etc/os-release
echo "caddy $(/usr/local/bin/caddy version 2>/dev/null | head -1) serves on ${PRETTY_NAME}"

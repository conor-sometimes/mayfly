#!/usr/bin/env bash
# The quickstart check: the named packages installed, the file landed, and we
# can read the distro. Exit 0 is a pass.
set -euo pipefail

command -v curl >/dev/null    || { echo "curl missing after install"; exit 1; }
command -v python3 >/dev/null || { echo "python3 missing after install"; exit 1; }
grep -q quickstart /etc/quickstart/hello.txt || { echo "file not copied"; exit 1; }

. /etc/os-release
echo "quickstart ok on ${PRETTY_NAME}"

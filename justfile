# mayfly tasks. `just` with no target lists them.

default:
    @just --list

# The full gate, matching the family's bar (banken): static analysis, the unit
# suite with the race detector, coverage, and the pure Nix checks. Individual
# pieces are their own recipes below. (fuzz and mutation are heavier, run them
# on their own; the per-distro VM runs need KVM.)
check: fmt-check vet lint errcheck gosec vulncheck race cover
    nix flake check

# Build the binaries and run the unit + property tests (no race; quick).
test:
    CGO_ENABLED=0 go build ./...
    go test ./...

# The unit + property suite with the race detector on and no caching: mayfly's
# matrix runner is concurrent (it boots several distros at once), so this is not
# optional. Needs a C toolchain (the race detector is cgo).
race:
    CGO_ENABLED=1 go test ./... -race -count=1

# Coverage of the suite.
cover:
    go test ./... -cover -count=1

# Format (write locally; `fmt-check` in the gate).
fmt:
    gofumpt -w .

fmt-check:
    gofumpt -l .

vet:
    go vet ./...

# staticcheck: the standard linter beyond vet.
lint:
    staticcheck ./...

# Every error return is checked or explicitly ignored.
errcheck:
    errcheck ./...

# Security static analysis. G204 (a subprocess with variable args) and
# G304/G703 (reading a path the config or manifest names) are inherent to a tool
# whose whole job is to run distro commands and read the images/scripts it is
# told to, inside a throwaway VM and run dir; they are excluded. Everything else
# (permissions, etc.) must be clean.
gosec:
    gosec -quiet -exclude=G204,G304,G703 ./...

# Known-vulnerability scan of the dependency graph.
vulncheck:
    govulncheck ./...

# Mutation testing: mutate the oracle (the trustworthy core - subtree
# attribution and the parsers) and confirm the suite catches each change. The
# strongest statement a testing tool can make about its own tests.
mutation:
    gremlins unleash ./internal/oracle

# Fuzz every parser that reads input the engine does not control (the audit
# log, a subject's OCSF jsonl, a distro's tool output, the guest serial log).
# Count-bounded (execs, not wall-clock) so a CI smoke is deterministic; the
# seed corpus alone runs under `just test`. Override the budget with N:
#   just fuzz              # 50000 execs/target (CI smoke)
#   just fuzz 5000000x     # a longer local sweep
fuzz N='50000x':
    #!/usr/bin/env bash
    set -euo pipefail
    export CGO_ENABLED=0
    for t in FuzzAuditExe FuzzAuditInt FuzzProcTreeDescendants FuzzOCSF FuzzPinDNFRepo FuzzTextHelpers; do
        echo "=== oracle $t ({{N}}) ==="
        go test -run='^$' -fuzz="^$t\$" -fuzztime={{N}} ./internal/oracle
    done
    echo "=== distrocheck FuzzParseStages ({{N}}) ==="
    go test -run='^$' -fuzz='^FuzzParseStages$' -fuzztime={{N}} ./cmd/distrocheck
    echo "=== matrix FuzzLastReport ({{N}}) ==="
    go test -run='^$' -fuzz='^FuzzLastReport$' -fuzztime={{N}} ./cmd/matrix

# e2e: a failing test still tears the VM down (the teardown trap's other branch).
# Needs KVM.
e2e-teardown:
    bash tests/e2e-teardown.sh "$PWD"

# Flake-rate soak: boot + install + test + teardown the smoke matrix R times and
# report the pass rate. This MEASURES flakiness (not a CI gate - a soak that must
# be 100% green every run is itself flake-sensitive). Needs KVM. Run on a quiet
# host; each run is a fresh VM. e.g. just soak 20
soak R='10':
    #!/usr/bin/env bash
    set -uo pipefail
    pass=0; fail=0; logdir="$(mktemp -d)"
    for i in $(seq 1 {{R}}); do
        if nix run .#matrix >"$logdir/run$i.log" 2>&1; then
            pass=$((pass + 1)); echo "run $i/{{R}}: PASS"
        else
            fail=$((fail + 1)); echo "run $i/{{R}}: FAIL (see $logdir/run$i.log)"; tail -4 "$logdir/run$i.log"
        fi
    done
    echo "soak: $pass/{{R}} passed, $fail failed  (logs: $logdir)"
    [ "$fail" -eq 0 ]

# Regenerate the committed distro table from the catalog.
readme:
    nix run .#readme > distros/TABLE.md

# Run lychee over every distro image URL and the README/doc links, so a dead
# mirror or moved image is caught before a VM run hits it.
links:
    lychee --no-progress README.md examples/**/*.md $(nix eval --raw .#packages.x86_64-linux.readme 2>/dev/null || true)
    nix eval --json .#distros --apply 'ds: builtins.concatLists (map (d: if d.image ? url then [ d.image.url ] else []) (builtins.attrValues ds))' \
      | tr -d '[]" ' | tr ',' '\n' | grep . | lychee --no-progress -

# Boot one distro end to end (needs KVM). e.g. just distro ubuntu-2404
distro name:
    nix run .#distro-{{name}}

# Boot every enabled distro (needs KVM).
matrix:
    nix run .#matrix

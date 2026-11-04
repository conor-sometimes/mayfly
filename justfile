# distro-matrix tasks. `just` with no target lists them.

default:
    @just --list

# Build the Go binaries and run the oracle's unit tests.
test:
    CGO_ENABLED=0 go build ./...
    go test ./...

# Format and vet the Go.
fmt:
    gofumpt -w .
    go vet ./...

# The full pure gate: Go, flake eval, and the README table check.
check:
    CGO_ENABLED=0 go build ./...
    go test ./...
    gofumpt -l .
    nix flake check

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

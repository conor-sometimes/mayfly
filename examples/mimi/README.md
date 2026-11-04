# mimi on the matrix

Does mimi, banken's eBPF process-telemetry sensor, attach and emit on a real
distro kernel?

mimi loads a tracepoint probe via CO-RE off the running kernel's own BTF and
streams exec events as OCSF-shaped JSON (the `mimi/2` schema banken ingests) to
a sink file. At home it is tested on a NixOS VM; this example proves the
fully-static musl ship binary does the same on a stock vendor kernel.

## What it does

- Subject: `mimi` (the fully-static musl release), installed at
  `/usr/local/bin/mimi` and run as the hardened `mimi.service` unit. The unit
  mirrors mimi's own NixOS module (`DynamicUser` + `CAP_BPF` / `CAP_PERFMON` /
  `CAP_SYS_ADMIN` / `CAP_DAC_READ_SEARCH`, full sandbox), adapted to a guest
  with no Nix. It writes its sink to `/var/log/mimi/events.jsonl`.
- Test: `check.sh` waits for the unit to be active and stable (no crash-loop),
  waits for the sink to fill (the probe loaded), runs a canary
  (`cp "$(command -v bash)" /tmp/mimicanary && /tmp/mimicanary -c true`), and
  asserts the captured event is `schema == mimi/2`, `kind == exec`,
  `process.file.path == /tmp/mimicanary`. On failure it dumps the journal and
  the sink tail.
- Distros: Ubuntu 24.04 only for now (the walking skeleton). Add more with one
  `.enable = true` each once this is green.

## Run it

```sh
nix run .#distro-ubuntu-2404     # Ubuntu 24.04 (needs KVM)
```

## Dependency

This example takes the mimi repo as a flake input
(`github:conor-sometimes/mimi`, for `mimi-musl`). mimi's repo is public; the
build is hermetic (fenix nightly + vendored build-std for the BPF target), so
no eBPF toolchain is needed on the host beyond Nix and KVM.

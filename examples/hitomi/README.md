# hitomi on the matrix

Does the hitomi binary, as shipped, detect correctly on each real distro?

hitomi is banken's YARA content-scanner sibling: a musl static binary. This
example installs it with its rule set and the EICAR accuracy suite, then runs
the suite on every enabled distro and requires a perfect sweep: 100% true
positives, 0% false positives.

## What it does

- Subject: `hitomi` (the musl static release), plus its rules and the EICAR
  suite, staged as tars and unpacked by `setup.sh` into `/etc/hitomi/rules` and
  `/opt/suite`.
- Test: `run-suite.sh` scans every sample and fails the distro on any missed
  detection or any false positive.
- Distros: Ubuntu 24.04 and Fedora 44 (add more with one `.enable = true` each).

## Run it

```sh
nix run .#matrix                 # both distros
nix run .#distro-ubuntu-2404     # one
```

## Dependency

This example takes the hitomi repo as a flake input
(`github:conor-sometimes/hitomi`, for `hitomi-musl` and `testdata/`). hitomi's
public repo is pending (see the design doc's open question 5). Until it is
reachable, the example is here as the intended shape; it evaluates and runs once
the input resolves, exactly as banken's own `tests/distros/matrix.nix` consumes
mayfly.

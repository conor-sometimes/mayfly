# Design: a fixed result protocol, and the `mayfly` guest CLI

Status: decided 2026-11-05, staged migration below. The normative contract is
[docs/protocol.md](../protocol.md) (`mayfly/1`); this document is the
rationale, the shape, the transport choice and the migration plan, and cites
the spec by section. Line references are to mayfly HEAD 950e032.

Why two files: a spec that consumers and mayfly itself implement against, and
that a conformance suite cites by section number, must not carry a migration
plan that goes stale or rationale that an implementer has to read around. The
spec is short and numbered; everything arguable lives here.

## Decision in one paragraph

mayfly becomes project-agnostic. The engine keeps six irreducible jobs: boot,
provision, run one consumer entrypoint, collect results, render the matrix,
tear down. Everything a consumer says to the engine goes through one typed
program, the `mayfly` CLI, shipped into the guest as a subcommand of mayfly's
own binary: `mayfly pass|fail|skip|error|info` to report, `mayfly detect` (and
the small `sandbox`/`lsm` helpers) to do the hard shared work. The consumer's
entrypoint is one file of thin glue that calls those subcommands. The wire
between guest-side Go and host-side Go is mayfly's internal business and is not
a consumer contract (spec 7). The check-kind vocabulary (`detect`, `script`,
`exit`, `sandbox`, `lsm`) and every consumer-shaped field in the engine go
away. mayfly's own engine and CLI are conforming implementations of the spec,
tested by the same conformance suite as the consumers (spec 10).

This is not "mayfly becomes a pile of shell scripts". Structure, validation and
logic live in Go and are unit-tested there; a consumer's entrypoint is a few
`mayfly` calls and never parses or formats anything (spec 8.3, 9).

## Why

The guest runner grew a per-kind switch (cmd/distrocheck/main.go:514-527) and
started carrying one consumer's shape: `emulSkip = "BANKEN-EMUL-SKIP"` (:586),
`testUser = "alice"` (:36), `drainQuiescent` for one consumer's batch mode
(:562-582), `Subject.Scan` documented as "banken's oneshot scan"
(internal/report/report.go:52), banken named in the public option schema
(lib/module.nix:61, :65, :87). Each new consumer would add a case.

A real bug showed the cost. A detect check's emulation never ran (the payload
sat under a 0700 home the test user could not traverse), and because
`runEmulation` discards the launched command's exit status (`out, pid, _ =`,
:650 and :653), exit 126 was indistinguishable from "ran, fired nothing". With
`expect = []` that is `oracle.Equal([], [])`, a PASS (:639). On banken's
145-check matrix every expect-a-rule check failed and every expect-nothing check
passed (commit 950e032), which read as a detection gap, not as the harness never
running an attack. The engine asserted a precondition it never checked. The
spec makes "nothing ran, reported green" structurally impossible: a mandatory
plan declared before boot (spec 4), five reconciliation rules (spec 5), an
`error` verdict distinct from `fail` (spec 6.1), a completion marker the host
demands (spec 7.4), and exit status as evidence in `mayfly detect` (spec 9.4.5).

The `script` kind already is most of the target (:662-681): run a script, read
`<id> PASS|FAIL|SKIP` lines against a declared `ids` plan, and report a silent
id as a failure (:666-673). The spec promotes that shape, moves the emitting
side into Go, adds `error`, and makes the plan mandatory everywhere.

## The shape

```
consumer entrypoint (bash, one file)
   |  calls
   v
mayfly pass|fail|skip|error|info|detect|sandbox|lsm      guest-side Go (cmd/distrocheck)
   |  appends records to $MAYFLY_RESULTS (internal, spec 7)
   v
mayfly run (the guest runner)                            guest-side Go
   |  reconciles against the plan (spec 5), writes `done` (spec 7.4), emits
   v  transport (internal: ssh stdout today, results file over scp next, virtio-serial later)
matrix (host runner)                                     host-side Go (cmd/matrix)
   |  re-reconciles (spec 5.6), renders distro x check grid (spec 6.3), exit code
```

A banken entrypoint, whole (spec 8.3 and 9.4):

```sh
#!/usr/bin/env bash
# One `mayfly detect` per planned emulation; the expected rule set per emulation
# is a fixture file, one rule per line (empty file = must fire nothing).
set -u
for id in $MAYFLY_PLAN; do
  args=()
  while read -r rule; do args+=(--expect "$rule"); done < "/opt/banken/fixtures/$id.rules"
  [ ${#args[@]} -gt 0 ] || args=(--expect-none)
  mayfly detect "$id" "${args[@]}" --as "$MAYFLY_TEST_USER" \
    --findings '/var/lib/banken/findings/*.jsonl' --scan 'banken scan' \
    -- bash "/opt/banken/emulations/$id.sh"
done
```

mimi's and hitomi's entrypoints are unchanged: they use the `test` sugar (spec
4.5, 9.3), defined in terms of the same protocol.

### Why the plan lives in Nix (spec 4.1)

1. Typed and validated at eval. Today `checks` is `types.attrsOf types.attrs`
   (lib/module.nix:97), i.e. untyped; the new option is a submodule and a typo
   fails before anything boots.
2. Known before boot. Today the grid's columns are whatever ids the reports
   happened to contain (cmd/matrix/main.go:402-410), so a guest that never
   booted has no columns and nothing to be red in.
3. `when` resolves per distro in Nix (lib/mkMatrix.nix:128-129), which is what
   makes the `-` cell possible and keeps that logic out of every entrypoint.
4. A plan emitted as the entrypoint's first line is defeated by the exact
   failure this design exists for: an entrypoint that dies before line one has
   no plan, and silence is green again.

The no-Nix CLI path (`-test`, cmd/matrix/main.go:252-258) gets the implicit
plan `{ test }`.

### Why strict exit status in `mayfly detect` (spec 9.4.5)

All 160 of banken's emulations end in an explicit `exit 0` and none uses
`set -e` (checked 2026-11-05 in ~/Projects/nix/banken/tests/emulations); an
emulation whose `nc` fails already masks it with `|| true`. Exit 77 for skip is
automake's convention and replaces the `BANKEN-EMUL-SKIP` marker: `echo
BANKEN-EMUL-SKIP: ...; exit 0` becomes `echo ...; exit 77` (90 files,
mechanical). `--expect-none` is required so "forgot to say what should fire"
cannot pass (spec 9.4.1).

### Why `sandbox` and `lsm` stay, as Go subcommands (spec 9.5)

They are generic systemd and LSM facts, not project-specific; their parsers
exist and are tested (`oracle.Exposure`, internal/oracle/parse.go:123-132;
`LinesContaining`, :60-68); each is ~15 lines on today's :692-715. Keeping them
as typed subcommands is consistent with "consumers call Go, not scripts".

## Module options after the change

```nix
{
  install = { ... };              # unchanged; `installs` for several subjects
  entrypoint = ./run.sh;          # the protocol tier: runs as root, reports via `mayfly` (spec 8)
  checks = {                      # the plan (spec 4); attrsOf (submodule { when; xfail; })
    revshell = { };
    portscan.when = d: d.pm == "apt";
    knownGap.xfail = true;        # later (spec 6.2)
  };
  test = ./check.sh;              # the simple tier (spec 4.5); exclusive with entrypoint/checks
  audit = true;                   # provisioning: auditd on, `-a task,never` stripped, rules loaded after files land
  testUser = "alice";             # provisioning: create this unprivileged user; exported as $MAYFLY_TEST_USER
}
```

- `test` with `entrypoint` or `checks` is an eval-time assertion failure;
  `checks` without `entrypoint` likewise.
- `audit` replaces today's inference from the presence of a `detect` kind
  (`needsAudit`, :361-368). It keeps the engine-phase ordering learned by
  pain: `auditSetup` before provisioning (:374-389, :115-124), rule load after
  a subject's files land (:394-410, called at :438). Default false.
- `testUser` replaces the `alice` constant (:36) and the `useradd` at :416;
  the payload-opening chmods at :426-431 run only when it is set. Default null.
- Removed: `checks.<id>.kind|run|as|expect|ids|unit|match|tools|optional`,
  `install.ocsf`, `install.scan` (now `mayfly detect` flags). `tools`/
  `optional` become `--needs` on `mayfly detect` or `command -v X || mayfly
  skip <id> "no X"` in the entrypoint.
- Environment: today's `MAYFLY_DISTRO`/`MAYFLY_BINARY` (:812-815) extended to
  the table in spec 8.2; the positional `pm distro user` argv of the `script`
  kind (:663) goes away.

## Crossing the VM boundary

The consumer never crosses it. Conor's ideal, "report to mayfly's Go binary
directly", is what happens: the consumer calls guest-side Go (`mayfly pass`),
guest-side Go hands records to host-side Go (`matrix`). Both ends are mayfly's
own code from the same build (lib/mkMatrix.nix:51-59 builds `distrocheck` and
`matrix` from one source; spec 2.4), and the payload that carries the guest
binary is already scp'd in (:120; cmd/matrix/main.go:708-719). The transport
between them is internal (spec 7.5), with three options in order of robustness:

(a) **ssh stdout scrape** (current). The host runs `sudo distrocheck` over ssh
and keeps the last `MATRIX-REPORT:` JSON line (:551-558, :784-798); the guest
prints a full report after every check (:103) so a dying guest still reports
how far it got. Simplest; no new code. Depends on the ssh session living for
the whole run, and the report shares a stream with the guest's chatter.

(b) **results file pulled over scp** (near-term default). `mayfly <verb>`
appends one record per call to `$MAYFLY_RESULTS` (a jsonl file under the
payload dir, O_APPEND, one write per record). After the entrypoint exits the
runner appends `done` and the host pulls the file with the scp channel it
already has (`scp` at :769-774, today push-only; add a pull). The file survives
the runner or the ssh session dying mid-run; the host still needs one scp round
trip. Structured, partial progress for free, no stream sharing, no new
transport.

(c) **virtio-serial** (robust upgrade). QEMU wires a host unix socket to a
guest char device, out of band from ssh and guest networking:

```
-chardev socket,id=results,path=WORK/results.sock,server=on,wait=off
-device virtio-serial-pci
-device virtserialport,chardev=results,name=mayfly.results
```

The guest sees `/dev/virtio-ports/mayfly.results`; the runner writes records as
they happen and the host reads the socket. Nothing is lost if sshd stops
answering (arch and alpine have already failed to provision on an ssh handshake
reset), and it is the nearest thing to a literally direct call. banken's own
pre-mayfly harness already reports this way in spirit (JSON on the serial
console, launched from the cloud-init seed; banken tests/distros/README.md),
so the pattern is proven on these images. Starting the entrypoint still goes
over ssh (or a cloud-init runcmd); only the results path moves.

The completion marker (spec 7.4) is what makes every transport safe: the guest
runner, never the consumer, writes `done` after reconciliation; the host treats
its absence as a crash. Silence is never green on any of the three.

Recommendation: ship (b) in the migration, keep (a) as the fallback when the
pull fails, schedule (c) once the virtio port's udev name has been checked on
every catalog image.

## Host side

- `report.Check` gains `Status string` (`pass|fail|skip|error|xfail|xpass`) and
  loses the `Pass`/`Skip` bools (report.go:19-24). Guest and host ship from one
  build, so no compatibility shim (spec 2.4).
- Grid cells (cmd/matrix/main.go:400-467): the seven of spec 6.3. Columns come
  from the plan, in plan order, not from first-seen ids. Today's `ERR` ("no
  report") becomes ERROR on every planned id, with `failed_phase`/`error` in
  info (:487-494 already records them for host phases).
- Exit code: 1 if any FAIL, ERROR or XPASS (spec 6.3); 2 on usage or no KVM
  (:113-119).
- Engine-phase failures in the guest stop reporting a synthetic `setup` check
  (:127-136) and instead mark every planned id `error "not reached: ..."`
  (spec 5.7).

## Migration

Each stage is its own PR. `just check` (gofumpt, vet, staticcheck, errcheck,
gosec, govulncheck, race, cover, `nix flake check`) is the code gate for all of
them; from Stage 1 on, `nix flake check` includes the conformance suite. The VM
gates are named per stage, against two baselines on real Ubuntu:

- **hitomi is the clean primary baseline.** Its checks are unambiguous (EICAR
  suite, 100% TP / 0% FP) and use the simple `test` path, so it is the fastest,
  least-ambiguous signal that the protocol, plan and CLI are right. Use it first
  on every stage that touches the common path.
- **banken is the detect-path baseline.** It is the only consumer that exercises
  the `detect` subtree-attribution path, so Stages 0 and 2 (which refactor that
  path) must reproduce its 145-check matrix (green as of 950e032). Its mayfly
  module is committed at banken `tests/distros/matrix.nix` on main.

### Stage 0: exit status is evidence (no protocol change, ship first)

- cmd/distrocheck/main.go `runEmulation` (:645-658): return the error.
  `detect` (:591-640): if exit is 126/127, or non-zero with no descendant in
  the subtree beyond the launcher, record `c.check(id, false, "emulation did
  not run: ...")` with the output tail, before the exact-set compare.
- Factor the verdict into a pure `oracle.EmulationRan(exit int, descendants
  int) (ran bool, why string)` with a table test.
- Gate: `nix run .#distro-ubuntu-2404` on the banken module, all green. Then
  the negative canary: with the chmod loop at :426-431 disabled locally, every
  `as = "user"` check must go red with "did not run", not green.

### Stage 1: the spec, the plan, the `mayfly` CLI, the conformance suite (transport (a) kept)

- internal/report/report.go: `Check{ID, Status, Detail}`; `Config` gains
  `Entrypoint`, `Plan []PlanEntry{ID, XFail}`, `TestUser`, `Audit`; `CheckSpec`
  and its per-kind fields go. Add the record types of spec 7.2-7.4 with the
  `schema` member.
- internal/oracle: `Reconcile(plan []PlanEntry, records []Record, exit int,
  done bool) []Check`, pure, with a table test per rule 5.1-5.5, 5.7 and 7.4.2;
  `ParseRecords` fuzzed like `parseStages` is today
  (cmd/distrocheck/fuzz_test.go), covering 2.2 and 7.1.2.
- cmd/distrocheck/main.go: dispatch on `os.Args[1]`. `run` is today's `main`
  minus the kind switch: provision, install itself at `/usr/local/bin/mayfly`
  (spec 9.1), export spec 8.2's environment (incl. `MAYFLY_PROTOCOL`), run
  `entrypoint` (or the `test` sugar, spec 9.3) as root with stdout+stderr to a
  log file, `Reconcile`, write `done`, emit the report on stdout as today.
  `pass|fail|skip|error|info` validate per spec 9.2 and append a record to
  `$MAYFLY_RESULTS`.
- lib/module.nix: `entrypoint`, typed `checks` (`attrsOf (submodule { when;
  xfail; })`), `audit`, `testUser`; the exclusivity assertions; drop
  `ocsf`/`scan` from `subjectModule`. `xfail` set with no engine support is an
  eval error (spec 6.2.3) until implemented.
- lib/mkMatrix.nix: `checkModel` (:93-109) becomes a plan entry (`id`,
  `xfail`, `when`); stage `entrypoint` at `checks/entrypoint`; `checksForDistro`
  (:128) filters the plan.
- cmd/matrix/main.go: `Distro.Checks` carries the plan; `summarize` and
  `matrixTable` render spec 6.3; exit-code rule; host-side re-reconciliation
  (spec 5.6).
- `tests/conformance/`: the suite of spec 10, wired into `nix flake check`.
  Pure parts (E3-E7, H1-H3, H6) run as Go tests against fixtures; the VM parts
  (E1, E2, E8, C1-C5) run through `examples/protocol/`, a consumer whose
  entrypoint exercises every verb plus a deliberately unplanned id and a
  deliberately unreported id, with the expected grid (two ERROR cells)
  asserted by a test in cmd/matrix from the `-json` report.
- README: "Two test tiers" and "The module API" rewritten and pointing at
  docs/protocol.md; `examples/` unchanged (all use `test`).
- Gate: `nix run .#matrix` (smoke) green; `nix run .#distro-ubuntu-2404` for
  examples/caddy, mimi, hitomi green with their configs untouched;
  `examples/protocol/` yields exactly its expected grid.

Between Stage 1 and Stage 2 the engine has no detect path: the Go `detect`
code is unreachable (Nix no longer emits `kind`). That gap is acceptable only
because nothing committed uses `kind` checks (every example uses `test`, and
banken main still runs its own harness); land Stages 1 and 2 back to back.

### Stage 2: `mayfly detect`, `sandbox`, `lsm`; kinds removed

- cmd/distrocheck: `detect` subcommand per spec 9.4, built from
  `detect`/`runEmulation`/`drainQuiescent`/`findings`/`drive`/`auditSince`/
  `auditExecsSince` (:530-658, :717-758) with `--findings` replacing
  `Subject.OCSF`, `--scan` replacing `Subject.Scan`, exit 77 replacing
  `emulSkip`; the precondition checks of 9.4.2 (`oracle.StatusField` on
  `auditctl -s`, parse.go:81-88). `sandbox` and `lsm` per spec 9.5 from
  :692-715. Delete `runCheck`, `script`, `exitCheck`, `parseStages`,
  `checkIDs`, `checkTools` and the `tools`/`optional` install path (:190-212,
  :243-274).
- `needsAudit` (:361) becomes the `audit` option; `auditSetup` and
  `loadAuditRules` run on it, unchanged.
- Tests (conformance H4, H5): `detect` end to end with a fixture audit log and
  fixture findings (an injected reader, or `--audit-log`/`--launcher-pid`
  behind a test build tag), one case per row of spec 9.4.5 and one for 9.4.1.
- Gate: banken's module rewritten to `entrypoint` + plan (the example above)
  on `nix run .#distro-ubuntu-2404`: the 145-check grid identical to the
  Stage 0 baseline, then the Stage 0 negative canary again, expecting ERROR
  cells this time.

### Stage 3: transport (b), results file over scp

- cmd/distrocheck `run`: `$MAYFLY_RESULTS` = `<payload>/results.jsonl`;
  append `done` after reconciliation; keep the stdout report as fallback.
- cmd/matrix: add an scp pull (:769-774) of `results.jsonl` and the entrypoint
  log after the `run` phase; prefer the file, fall back to the stdout scrape;
  apply spec 7.4.2 on the host.
- Conformance E5 gains a VM case: an entrypoint `kill -9`'d mid-way yields
  ERROR on the unreported ids and keeps the reported ones (spec 5.4).
- Gate: smoke and banken grids unchanged; `tests/e2e-teardown.sh` still
  passes; the E5 case passes.

### Stage 4: de-banken the engine

- Delete the `alice` constant and the hard-coded `useradd` (:36, :416-431) in
  favour of `testUser`; purge "banken" from lib/module.nix, report.go and the
  README's engine sections (examples may still say banken); rename
  `cmd/distrocheck` to `cmd/mayfly-guest` if wanted (cosmetic; update
  lib/mkMatrix.nix:57 and :120).
- Gate: `grep -rni banken cmd internal lib` returns nothing; smoke green.

### Stage 5: conform the consumers (conformance C1-C6)

- banken: commit the mayfly module (`entrypoint` as above, plan generated from
  the emulation set, `audit = true`, `testUser = "alice"`, `installs` for mimi
  and banken); `sed` the 90 `BANKEN-EMUL-SKIP` emulations to `exit 77`; add
  one positive control (C6); retire tests/distros once the mayfly grid matches
  it on the 8 distros.
- mimi, hitomi: no change required (`test` sugar). Optional: hitomi's
  run-suite.sh adds `mayfly info false_negatives $fn` and `false_positives
  $fp`.
- Each consumer becomes a case in `tests/conformance/` (its module evaluated
  against C1, C3, C4 statically; C2, C5 by a one-distro run on the KVM host).
- Gate: each consumer's full matrix on hokkaido green, per-distro counts
  recorded in the task annotation.

### Stage 6 (later): transport (c), virtio-serial

- cmd/matrix `startQEMU` (:624-651): the three QEMU args above; a goroutine
  reading the socket into the same record parser. cmd/distrocheck `run`: write
  each record to `/dev/virtio-ports/mayfly.results` when present.
- Gate: every catalog image exposes the port under that name (check once per
  image, record in distros/catalog.nix); a run with sshd stopped after the
  entrypoint starts still produces a full grid.

## Non-goals

mayfly does not define what a project tests or what a pass means; that stays
with the project and is why the vocabulary is only about what a grid cell can
say. Host-side actions mid-test (snapshots, reboots, a second VM or disk) are
future engine primitives, orthogonal to this protocol; "the engine knows
nothing project-specific" is not a vow never to add one.

## Decisions (resolved 2026-11-05)

1. **Baselines / banken's module.** banken's mayfly module is committed at banken
   `tests/distros/matrix.nix` on main (the consume-mayfly and mimi-source PRs
   merged, 145 checks green). hitomi is the clean primary baseline (simple `test`
   path, unambiguous pass/fail); banken is the detect-path baseline. Consumer
   configs stay in their own repos, not mayfly `examples/`.
2. **`sandbox`/`lsm` are Go subcommands** (spec 9.5), not example shell -
   consistent with "consumers call typed Go, not scripts".
3. **Strict emulation exit rule** (spec 9.4.5): any non-zero exit other than 77
   is `error`. Enforced (the corpus is 160/160 `exit 0`, no `set -e`).
4. **Transport (c), virtio-serial: Stage 6.** Pull forward only when we take on
   arch/alpine (which fail on an ssh handshake reset and would need the
   out-of-band channel).
5. **Rename `cmd/distrocheck` -> `cmd/mayfly-guest`** in Stage 4.
6. **`xfail` deferred** (spec 6.2.3 eval-rejects it) until a consumer asks.

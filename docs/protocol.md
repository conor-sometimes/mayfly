# The mayfly result protocol, version `mayfly/1`

Status: normative. This document specifies the contract between a consumer's
tests and the mayfly engine. mayfly's own engine (cmd/matrix, cmd/distrocheck)
and the `mayfly` guest CLI are conforming implementations held to this
specification; they are not exempt from it. The rationale and the migration
plan are in [design/result-protocol.md](design/result-protocol.md).

The key words MUST, MUST NOT, SHOULD, SHOULD NOT and MAY are to be interpreted
as in RFC 2119. Section numbers are stable; conformance tests cite them.

## 1. Roles and terms

- **Engine**: boots a guest, provisions it, runs the entrypoint, collects
  records, reconciles them against the plan, renders the grid. Host-side
  (`matrix`) and guest-side (`mayfly run`) halves are both the engine.
- **Consumer**: a project under test. It declares a plan and provides an
  entrypoint.
- **Entrypoint**: the one program the engine runs in the guest, as root, once
  per distro.
- **`mayfly` CLI**: the guest-side program a consumer calls to report. The
  only conforming way for a consumer to produce a record.
- **Plan**: the set of check ids a run must account for on a distro, with
  per-id modifiers.
- **Record**: one reported fact (a result, an info pair, or completion).
- **Verdict**: the status of one check id on one distro after reconciliation.

## 2. Versioning

2.1. Every record MUST carry `"schema":"mayfly/1"`.

2.2. An engine MUST ignore a record whose `schema` it does not implement, and
MUST NOT attempt to interpret it. The plan rules (section 5) then make the
consequence visible: an ignored result leaves its id unreported (ERROR), an
ignored `done` leaves the run incomplete (section 7.4). Silence is never green,
so ignoring is safe. An engine SHOULD count ignored records under the info key
`protocol/ignored`.

2.3. A change that alters the meaning of an existing member, verdict or rule
MUST bump the major version (`mayfly/2`). Adding an optional member or a new
record type MAY keep the version.

2.4. The `mayfly` CLI and the engine that reads its records MUST be built from
the same mayfly revision. A consumer MUST NOT ship its own copy of the CLI.

## 3. Identifiers

3.1. A check id and an info key MUST match:

```
id       = ALNUM *( ALNUM / "_" / "." / "/" / "-" )
ALNUM    = %x30-39 / %x41-5A / %x61-7A
```

3.2. Ids are case-sensitive and MUST be unique within a plan. An id MUST NOT be
longer than 128 bytes. The ids `protocol` and `test` are reserved: `protocol`
for engine-synthesised errors about the run (section 5), `test` for the
single-id sugar (section 4.5).

## 4. The plan

4.1. A consumer MUST declare its plan to the engine before the guest boots. In
mayfly the plan is the `checks` option of the Nix module; the engine MUST NOT
accept a plan from the entrypoint's output or from a record.

4.2. A plan entry has an id (3.1) and two OPTIONAL modifiers: `when`, a
predicate over the distro that excludes the id from that distro's plan; and
`xfail` (section 6.2).

4.3. The engine MUST resolve `when` per distro before boot and MUST pass the
resulting id list to the entrypoint as `MAYFLY_PLAN` (section 8.2). An id
excluded by `when` is rendered `-` and MUST NOT be reported; a record for it is
an unplanned id (rule 5.2).

4.4. A plan MUST contain at least one id for at least one distro.

4.5. Sugar: a consumer MAY give a single script instead of a plan and
entrypoint (`test` in the Nix module). The engine MUST treat this as the plan
`{ test }` and an entrypoint whose exit status is mapped by section 9.3; the
script MAY still call the `mayfly` CLI for `info` but MUST NOT report `test`
itself.

## 5. Reconciliation rules

After the entrypoint exits (or is found to have died), the engine MUST derive
exactly one verdict per planned id by these rules, in this order. "Result"
means a `result` record (section 7.2) for an id.

- **5.1 Unreported.** A planned id with no result MUST become ERROR with the
  detail `not reported`, or the detail from 5.4 if that applies.
- **5.2 Unplanned.** A result whose id is not in this distro's plan MUST be
  discarded and MUST produce an ERROR verdict on the reserved id `protocol`
  naming the stray id. The CLI MUST also refuse the call (section 9.2).
- **5.3 Empty.** A run with no result records MUST yield ERROR on every planned
  id (5.1 covers this; the engine MUST additionally mark the run as failed even
  if the plan is empty for this distro).
- **5.4 Crash.** If the entrypoint exited non-zero, or the run has no `done`
  record (7.4), every id without a result MUST become ERROR with a detail
  naming the exit status (or `no completion`) and the last lines of the
  entrypoint's log. Ids that have a result MUST keep it: a record is a complete
  fact and is not retracted by a later crash.
- **5.5 Duplicate.** A second result for an id that already has one MUST be
  discarded and MUST produce an ERROR verdict on `protocol` naming the id. The
  first result stands.

5.6. The engine MUST apply these rules on the guest side (before transport)
and MUST re-apply them on the host side over whatever records arrived, so a
transport that loses records cannot turn a red cell green.

5.7. An engine-phase failure before the entrypoint starts (package install,
provisioning) MUST yield ERROR on every planned id with the detail `not
reached: <phase>: <reason>`.

## 6. Verdicts

### 6.1 The four outcomes a consumer reports

| Outcome | Applies when | Fails the run |
| --- | --- | --- |
| `pass` | the subject was exercised and did what the check asserts | no |
| `fail` | the subject was exercised and did not | yes |
| `skip` | the check was not exercised by a decision taken beforehand: it does not apply here, or a tool it needs is absent | no |
| `error` | the check could not be carried out: a precondition failed, something the check needs was unreadable or not running, the thing to exercise could not be executed | yes |

6.1.1. A consumer MUST report `error`, not `fail`, when the subject was not
judged. A consumer MUST NOT report `skip` after the subject was exercised.

6.1.2. `skip` and `error` MUST carry a non-empty detail (the reason). `pass`
and `fail` MAY.

6.1.3. The engine MUST NOT infer `pass` from the absence of anything. The only
way an id is PASS is a `pass` result for it (or 4.5 sugar on exit 0).

### 6.2 `xfail` (optional modifier)

6.2.1. `xfail` is a plan modifier, never an outcome. A consumer MUST NOT report
"expected to fail"; it reports what happened.

6.2.2. The engine MUST combine: `fail` + xfail -> XFAIL (does not fail the
run); `pass` + xfail -> XPASS (fails the run). `skip` and `error` are
unaffected by xfail.

6.2.3. An engine MAY omit xfail support entirely in `mayfly/1`; if it does, it
MUST reject a plan that sets it at eval time rather than ignore it.

### 6.3 Rendered verdicts

The grid vocabulary is exactly: `PASS`, `FAIL`, `SKIP`, `ERROR`, `XFAIL`,
`XPASS`, `-`. The run's exit status MUST be non-zero if any cell is FAIL,
ERROR or XPASS.

## 7. Records and the wire

Records are mayfly's internal wire between the guest-side and host-side
engine. A consumer MUST NOT write records directly; it calls the CLI
(section 9). Implementations are checked against this grammar.

### 7.1 Framing

7.1.1. A record is one JSON object (RFC 8259) on one line, UTF-8, terminated by
LF, with no embedded CR or LF. Member order is not significant. Unknown members
MUST be ignored.

7.1.2. In a results file (section 7.5 (b)) each line is a record. On a stdout
transport (7.5 (a)) a record line is prefixed:

```
framed   = *WSP "mayfly:" record LF
record   = JSON-object        ; as constrained by 7.2-7.4
WSP      = SP / HTAB
```

A line that does not begin (after optional whitespace) with `mayfly:` is not
a record and MUST be ignored. A line that begins with `mayfly:` but does not
parse as a record MUST produce an ERROR on `protocol` naming the line number.

### 7.2 `result`

```
{"schema":"mayfly/1","t":"result","id":<id>,"status":<status>,"detail":<string>}
status = "pass" / "fail" / "skip" / "error"
```

`id` MUST satisfy 3.1. `detail` MUST be present (MAY be `""` for pass/fail;
MUST be non-empty for skip/error). The engine MUST truncate `detail` to 400
bytes with a trailing `...` when rendering, and MUST NOT truncate it in the
stored report.

### 7.3 `info`

```
{"schema":"mayfly/1","t":"info","k":<id>,"v":<string>}
```

`k` MUST satisfy 3.1. A repeated key is last-writer-wins. Info is recorded,
never judged: it MUST NOT affect any verdict or the exit status.

### 7.4 `done`

```
{"schema":"mayfly/1","t":"done","exit":<integer 0-255>}
```

7.4.1. The guest-side engine MUST write exactly one `done` record, after the
entrypoint has exited and after guest-side reconciliation, carrying the
entrypoint's exit status. A consumer MUST NOT write `done`; the CLI MUST NOT
offer a way to.

7.4.2. The host-side engine MUST demand `done`. A record stream without it
MUST be reconciled under rule 5.4 with the detail `no completion`, and the run
MUST be marked failed.

### 7.5 Transports

An engine MUST implement at least one of these and MUST apply 5.6 and 7.4.2
regardless of which carried the records.

- (a) **stdout**: framed records (7.1.2) on the ssh session that runs the
  guest-side engine. The guest MAY repeat its full record set after each new
  record so a dying session still carries partial progress.
- (b) **results file**: records appended one per line to the file named by
  `MAYFLY_RESULTS` (8.2), pulled by the host after the run over the existing
  file-copy channel. Appends MUST be a single write per record.
- (c) **virtio-serial**: records written as they occur to a guest character
  device backed by a host socket, independent of guest networking.

## 8. The entrypoint

8.1. The engine MUST run the entrypoint once per distro, as root, after
provisioning, with the working directory unspecified. The entrypoint's stdout
and stderr are a log: the engine MUST capture them, MUST attach their last
lines to any verdict it synthesises under 5.4 and 5.7, and MUST NOT interpret
them as records on transports (b) and (c). (On transport (a) the guest-side
engine, not the entrypoint, writes the framed lines.)

8.2. The engine MUST set these variables for the entrypoint:

| Variable | Value |
| --- | --- |
| `MAYFLY_PROTOCOL` | `mayfly/1` |
| `MAYFLY_DISTRO` | the distro attr |
| `MAYFLY_PM`, `MAYFLY_FAMILY` | the distro's package manager and family |
| `MAYFLY_PLAN` | the ids applicable to this distro (4.3), space-separated, in plan order |
| `MAYFLY_RESULTS` | the records file path; internal, a consumer MUST NOT set, read or write it |

and MUST set these when the corresponding option is set:

| Variable | Value |
| --- | --- |
| `MAYFLY_BINARY` | guest path of the installed binary |
| `MAYFLY_TEST_USER` | the provisioned unprivileged test user |

8.3. A consumer's entrypoint MUST report every id in `MAYFLY_PLAN` exactly once
through the CLI, SHOULD exit 0 after doing so, and MUST NOT parse or format
records itself. An entrypoint SHOULD be thin: provisioning belongs in the
module's `install` block, judging belongs in the CLI helpers or in the subject.

## 9. The `mayfly` CLI

9.1. The engine MUST install the CLI in the guest on `PATH` as `mayfly` before
the entrypoint runs. The CLI MUST be the same build as the engine (2.4).

### 9.2 Reporting subcommands

```
mayfly pass  <id> [detail ...]
mayfly fail  <id> [detail ...]
mayfly skip  <id> <reason ...>
mayfly error <id> <reason ...>
mayfly info  <key> <value ...>
```

- Each MUST write exactly one record (7.2 or 7.3) and exit 0, or write nothing
  and exit 2 on a usage error.
- `<id>` MUST satisfy 3.1 and MUST be in `MAYFLY_PLAN`; otherwise the CLI MUST
  print a one-line reason to stderr, write a `result` with status `error` for
  the reserved id `protocol`, and exit 2. (The engine then applies 5.2 as well.)
- `skip`/`error` with an empty reason MUST be a usage error.
- `detail`/`reason`/`value` is the remaining arguments joined by single spaces.
- The CLI MUST NOT read the plan, the previous records or the subject; it has
  no state. Duplicate detection is the engine's (5.5).

### 9.3 Exit-status mapping for the `test` sugar (4.5)

| Script exit | Verdict for `test` |
| --- | --- |
| 0 | PASS |
| 126, 127 | ERROR, detail `could not execute` plus the log tail |
| other | FAIL, detail the log tail |

### 9.4 `mayfly detect`

```
mayfly detect <id> ( 1*( --expect RULE ) / --expect-none )
              1*( --findings GLOB ) [--as USER] [--scan CMD]
              *( --needs TOOL ) [--timeout DURATION]
              -- CMD [ARG ...]
```

9.4.1. Exactly one of `--expect` (repeatable) or `--expect-none` MUST be given;
neither is a usage error (exit 2, nothing written). An empty expectation MUST
be explicit.

9.4.2. The subcommand MUST write exactly one `result` for `<id>` and exit 0 on
any outcome including `error`; it exits 2 only on a usage error. Preconditions
checked before `CMD` runs, each yielding `error` with a reason: the audit log
is readable; syscall auditing is enabled; `USER` exists. Each `--needs TOOL`
absent from `PATH` yields `skip` naming the tool.

9.4.3. `CMD` MUST run under a launcher process whose pid roots the attribution
subtree; with `--as USER` it MUST run as that user with a login-style `PATH`
including the sbin directories. `CMD` and all its descendants MUST be killed as
a group on `--timeout` (default 2 minutes).

9.4.4. Attribution MUST be by process identity: a finding counts for `<id>` if
and only if the finding's process pid, or failing that its parent pid, is in
the launcher's subtree as reconstructed from audit SYSCALL records written
since the launch mark. A finding is read from every file matching any
`--findings` glob; a finding's rule name is its `finding_info.analytic.name`
(OCSF). If `--scan CMD` is given the subcommand MUST run it until a settle
period adds no finding, both before the launch mark and after `CMD` exits,
bounded by a fixed maximum.

9.4.5. The exit status of `CMD` is evidence. The mapping MUST be:

| `CMD` exit | Result for `<id>` |
| --- | --- |
| 0 | `pass` if the fired rule set equals the expected set exactly, else `fail` |
| 77 | `skip`, detail the first non-blank line of `CMD`'s output |
| 126, 127 | `error`, detail `could not execute` plus the output |
| any other value, or timeout | `error`, detail the exit status plus the last lines of output |

A `pass` MUST NOT be reported for an expected-empty set unless `CMD` exited 0.

9.4.6. On `fail` the detail MUST include the fired set and the expected set,
and SHOULD include the distinct executables seen in the audit window and one
offending finding per unexpected rule.

### 9.5 `mayfly sandbox`, `mayfly lsm`

```
mayfly sandbox <id> --unit UNIT
mayfly lsm     <id> --match STRING
```

`sandbox` MUST report `pass` when `systemd-analyze security UNIT` rates the
unit SAFE or OK, `fail` for any other rating, and `error` when no rating can be
read (unit missing, tool missing). It SHOULD also write `info exposure/UNIT`.
`lsm` MUST report `fail` when the distro's SELinux or AppArmor logged a denial
containing STRING since boot, else `pass`; it reports `error` if neither log
source is readable.

### 9.6 Exit codes

| Code | Meaning |
| --- | --- |
| 0 | the subcommand did its job (whatever the reported outcome) |
| 2 | usage error; nothing written, or for an unplanned id the `protocol` error of 9.2 |

## 10. Conformance

Every requirement above is testable, and mayfly ships a conformance suite
(`tests/conformance/`, run by `nix flake check`) that exercises them. banken,
mimi and hitomi are conformance cases of that suite: each consumer's module and
entrypoint is run through the engine and checked for the consumer obligations
below. mayfly's own engine and CLI are held to the engine and CLI obligations
by the same suite; there is no exemption for being the reference.

### 10.1 A conforming engine

- E1. Accepts a plan only from the module (4.1), resolves `when` per distro
  (4.3), and renders every planned id, `-` for excluded ones (6.3).
- E2. Runs the entrypoint as specified in section 8 with the full
  environment of 8.2.
- E3. Parses records exactly per section 7, ignoring unknown schemas (2.2) and
  non-record lines (7.1.2), and never truncating stored detail (7.2).
- E4. Applies rules 5.1-5.5 and 5.7 in order, on both sides (5.6).
- E5. Demands `done` and treats its absence as a crash (7.4.2).
- E6. Never infers a pass (6.1.3); exit status per 6.3.
- E7. Implements `xfail` per 6.2 or rejects it at eval (6.2.3).
- E8. Installs the CLI from the same build (2.4, 9.1).

### 10.2 A conforming consumer

- C1. Declares a plan in the module with unique ids (3.2, 4.1, 4.4).
- C2. Reports every id in `MAYFLY_PLAN` exactly once, only through the CLI
  (8.3, 9.2), choosing the outcome by 6.1.
- C3. Never writes records, `done`, or `MAYFLY_RESULTS` (7.4.1, 8.2, 8.3).
- C4. Never reports "expected to fail"; uses the `xfail` modifier (6.2.1).
- C5. Exits 0 after reporting (8.3); provisioning lives in `install`.
- C6. (Detection consumers) Emulations exit 0 when they ran, 77 to skip, and
  never rely on any other exit value being judged (9.4.5). SHOULD keep one
  positive control in the plan.

### 10.3 What the `mayfly` CLI guarantees

- H1. Every reporting call writes exactly one well-formed record or nothing
  (9.2), with the schema of section 2.
- H2. Validates id syntax and plan membership at the call site (9.2).
- H3. Is stateless (9.2): no call depends on a previous call.
- H4. `detect` enforces the explicit-expectation rule (9.4.1), the precondition
  checks (9.4.2), identity attribution (9.4.4) and the exit-status table
  (9.4.5); it can be driven with a fixture audit log and fixture findings so
  every row of 9.4.5 has a test.
- H5. `sandbox`/`lsm` behave per 9.5.
- H6. Exit codes per 9.6.

# Schema tests: the "evalModules for the schema" half of the gold standard (the
# behaviour half is the gated per-distro VM runs, which need KVM). These are
# pure - no KVM, no network - so they run in `nix flake check`. A good config
# evaluates to the manifest shape the runner expects; a bad config is rejected
# at eval, naming the offending option; the catalog overlay resolves a shipped
# distro's package manager and family. Wired as the `schema` flake check.
{ pkgs, lib, dmlib }:
let
  run = module: dmlib.buildFromModules { inherit pkgs; modules = [ module ]; };
  manifestOf = module: (run module).manifest;
  force = x: builtins.deepSeq x true;
  # A thunk that fails at eval makes this true; one that evaluates makes it
  # false. Nix passes the argument lazily, so tryEval is what forces it.
  evalFails = thunk: !(builtins.tryEval thunk).success;

  base = { distros.ubuntu-2404.enable = true; };

  # Good configs (each picks a tier: `test` sugar, or `entrypoint` + `checks`).
  goodSubjects = (manifestOf (base // { install.packages = [ "curl" ]; test = "true"; })).subjects;
  single = (manifestOf (base // { install.packages = [ "curl" ]; test = "true"; })).subjects;
  plural = (manifestOf (base // {
    installs.a.packages = [ "curl" ];
    installs.b.packages = [ "jq" ];
    test = "true";
  })).subjects;
  distro0 = builtins.head (manifestOf (base // { test = "true"; })).distros;

  # The plan: `checks` become plan entries; `when` filters per distro.
  planManifest = manifestOf {
    distros.ubuntu-2404.enable = true;
    distros.fedora-44.enable = true;
    entrypoint = "true";
    checks = { a = { }; b = { when = d: d.pm == "apt"; }; };
  };
  planFor = name: (builtins.head (lib.filter (d: d.name == name) planManifest.distros)).plan;
  sugarPlan = (manifestOf (base // { test = "true"; })).plan;

  # Bad configs that MUST fail at eval (forced inside evalFails' tryEval).
  badType = (run (base // { install.binary = 123; test = "true"; })).manifest.subjects;
  badUnknown = (run (base // { notAnOption = true; })).eval.config;
  badCustom = (run { distros.nope.enable = true; test = "true"; }).manifest.distros;
  badBoth = (run (base // { test = "true"; entrypoint = "true"; checks.a = { }; })).manifest;
  badNeither = (run base).manifest;
  badXfail = (run (base // { entrypoint = "true"; checks.a.xfail = true; })).manifest;

  oneConcurrency = (run (base // { test = "true"; concurrency = 1; })).eval.config.concurrency;
  badConcurrency = (run (base // { test = "true"; concurrency = 0; })).eval.config.concurrency;

  checks = [
    { name = "good config evaluates"; ok = force goodSubjects; }
    { name = "single install -> one subject, no binary"; ok = builtins.length single == 1 && (builtins.head single).binary == ""; }
    { name = "installs plural -> two subjects"; ok = builtins.length plural == 2; }
    { name = "catalog overlay resolves pm + name"; ok = distro0.pm == "apt" && distro0.name == "ubuntu-2404"; }
    { name = "checks become a plan"; ok = builtins.length (planFor "ubuntu-2404") == 2; }
    { name = "when filters the plan per distro"; ok = builtins.length (planFor "fedora-44") == 1 && (builtins.head (planFor "fedora-44")).id == "a"; }
    { name = "test sugar plans the reserved id"; ok = builtins.length sugarPlan == 1 && (builtins.head sugarPlan).id == "test"; }
    { name = "concurrency accepts a positive int"; ok = oneConcurrency == 1; }
    { name = "wrong-typed install.binary is rejected"; ok = evalFails (force badType); }
    { name = "unknown option is rejected"; ok = evalFails (force badUnknown); }
    { name = "custom distro without image + pm is rejected"; ok = evalFails (force badCustom); }
    { name = "test and entrypoint together are rejected"; ok = evalFails (force badBoth); }
    { name = "neither test nor entrypoint is rejected"; ok = evalFails (force badNeither); }
    { name = "xfail is rejected in mayfly/1"; ok = evalFails (force badXfail); }
    { name = "concurrency = 0 (not positive) is rejected"; ok = evalFails (force badConcurrency); }
  ];
  failed = lib.filter (c: !c.ok) checks;
in
if failed == [ ] then
  pkgs.runCommand "mayfly-schema-test" { } ''
    printf 'mayfly schema: ${toString (builtins.length checks)} assertions passed\n' > "$out"
  ''
else
  throw "mayfly schema tests FAILED: ${lib.concatMapStringsSep "; " (c: c.name) failed}"

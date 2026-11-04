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

  # Good configs.
  goodSubjects = (manifestOf { distros.ubuntu-2404.enable = true; install.packages = [ "curl" ]; test = null; }).subjects;
  single = (manifestOf { distros.ubuntu-2404.enable = true; install.packages = [ "curl" ]; }).subjects;
  plural = (manifestOf {
    distros.ubuntu-2404.enable = true;
    installs.a.packages = [ "curl" ];
    installs.b.packages = [ "jq" ];
  }).subjects;
  distro0 = builtins.head (manifestOf { distros.ubuntu-2404.enable = true; }).distros;

  # Bad configs that MUST fail at eval (forced inside evalFails' tryEval).
  badType = (run { distros.ubuntu-2404.enable = true; install.binary = 123; }).manifest.subjects;
  badUnknown = (run { distros.ubuntu-2404.enable = true; notAnOption = true; }).eval.config;
  badCustom = (run { distros.nope.enable = true; }).manifest.distros;

  checks = [
    { name = "good config evaluates"; ok = force goodSubjects; }
    { name = "single install -> one subject, no binary"; ok = builtins.length single == 1 && (builtins.head single).binary == ""; }
    { name = "installs plural -> two subjects"; ok = builtins.length plural == 2; }
    { name = "catalog overlay resolves pm + name"; ok = distro0.pm == "apt" && distro0.name == "ubuntu-2404"; }
    { name = "wrong-typed install.binary is rejected"; ok = evalFails (force badType); }
    { name = "unknown option is rejected"; ok = evalFails (force badUnknown); }
    { name = "custom distro without image + pm is rejected"; ok = evalFails (force badCustom); }
  ];
  failed = lib.filter (c: !c.ok) checks;
in
if failed == [ ] then
  pkgs.runCommand "mayfly-schema-test" { } ''
    printf 'mayfly schema: ${toString (builtins.length checks)} assertions passed\n' > "$out"
  ''
else
  throw "mayfly schema tests FAILED: ${lib.concatMapStringsSep "; " (c: c.name) failed}"

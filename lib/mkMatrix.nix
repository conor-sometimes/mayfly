# buildMatrix turns an evaluated mayfly config into a running matrix: apps to
# run it, per-distro KVM checks, the generated distro table, and the manifest
# the host runner consumes. lib/default.nix evalModules the user's mayfly.nix
# (or flake options) against lib/module.nix and hands the result here.
#
# matrixSrc and catalogSet are baked in by the flake; cfg is the evaluated
# config (the root of the user's mayfly.nix).
{ pkgs
, lib ? pkgs.lib
, matrixSrc
, catalogSet
, cfg
}:
let
  # --- resolve distros: catalog-enable (merge the shipped entry), plus custom ---
  enabled = lib.filterAttrs (_: d: d.enable) cfg.distros;
  resolveOne = name: d:
    if catalogSet ? ${name} then
      let c = catalogSet.${name}; in
      {
        inherit name;
        title = if d.title != name then d.title else (c.title or name);
        inherit (c) pm image;
        family = c.family or "";
        packages = (c.packages or { }) // d.packages;
        inherit (d) boot;
      }
    else if d.image != null && d.pm != null then
      { inherit name; inherit (d) title pm family image packages boot; }
    else
      throw "mayfly: distro '${name}' is not in the catalog, so it needs `image` and `pm` set";
  resolved = lib.mapAttrs resolveOne enabled;
  distroList = lib.attrValues resolved;

  subject = cfg.install;
  subjects = cfg.installs;
  test = cfg.test;
  checks = cfg.checks;

  imageOf = d:
    if d.image ? drv then d.image.drv pkgs
    else
      pkgs.fetchurl ({ inherit (d.image) url; }
        // (if d.image ? sha512 then { inherit (d.image) sha512; } else { inherit (d.image) sha256; }));

  # --- normalize the consumer's subjects and checks ---
  subjectSet = (lib.optionalAttrs (subject != null) { ${subject.name or "subject"} = subject; }) // subjects;
  checkSet = (lib.optionalAttrs (test != null) { test = { kind = "exit"; run = test; }; }) // checks;

  # --- the Go binaries: distrocheck (guest) and matrix (host), static ---
  tools = pkgs.buildGoModule {
    pname = "mayfly";
    version = "0.1.0";
    src = matrixSrc;
    vendorHash = null; # no external modules
    env.CGO_ENABLED = 0;
    subPackages = [ "cmd/distrocheck" "cmd/matrix" ];
    doCheck = false; # the oracle's unit tests run in `nix flake check` (see flake.nix)
  };

  # --- lay out one subject's files at stable payload paths, and the manifest shape it maps to ---
  subjectModel = name: s:
    let
      unitFiles = (s.units or [ ]) ++ (lib.optional (s.unit or null != null) s.unit);
      # Keep the original unit filename (banken.service), under a per-subject
      # units/ dir to avoid collisions: the installed unit name is this basename.
      units = map (u: { rel = "subjects/${name}/units/${baseNameOf u}"; src = u; }) unitFiles;
      fileList = lib.imap0 (i: kv: { inherit (kv) name; guest = kv.name; src = kv.value; rel = "subjects/${name}/file${toString i}"; })
        (lib.attrsToList (s.files or { }));
      hasBinary = s ? binary && s.binary != null;
      hasSetup = s ? setup && s.setup != null;
    in
    {
      copies =
        lib.optional hasBinary { rel = "subjects/${name}/bin"; src = s.binary; mode = "0755"; }
        ++ map (u: { inherit (u) rel src; mode = "0644"; }) units
        ++ map (f: { inherit (f) rel src; mode = "0644"; }) fileList
        ++ lib.optional hasSetup { rel = "subjects/${name}/setup"; src = s.setup; mode = "0755"; };
      manifest = {
        inherit name;
        binary = if hasBinary then "subjects/${name}/bin" else "";
        install_path = s.installPath or "";
        units = map (u: u.rel) units;
        files = lib.listToAttrs (map (f: { name = f.guest; value = f.rel; }) fileList);
        setup = if hasSetup then "subjects/${name}/setup" else "";
        enable = (lib.optionals (s.start or true) (map baseNameOf unitFiles)) ++ (s.enable or [ ]);
        packages = s.packages or [ ];
        ocsf = s.ocsf or "";
        scan = s.scan or [ ];
      };
    };

  checkModel = id: c: {
    copies = lib.optional (c ? run && c.run != null) { rel = "checks/${id}/run"; src = c.run; mode = "0755"; };
    # the manifest entry, minus the `when` predicate (resolved per distro in Nix)
    manifest = {
      inherit id;
      kind = c.kind;
      run = if c ? run && c.run != null then "checks/${id}/run" else "";
      as = c.as or "";
      expect = c.expect or [ ];
      ids = c.ids or [ ];
      unit = c.unit or "";
      match = c.match or "";
      tools = c.tools or [ ];
      optional = c.optional or false;
    };
    when = c.when or (_: true);
  };

  subjectModels = lib.mapAttrsToList subjectModel subjectSet;
  checkModels = lib.mapAttrs checkModel checkSet;

  allCopies = lib.concatMap (m: m.copies) subjectModels
    ++ lib.concatLists (lib.mapAttrsToList (_: m: m.copies) checkModels);

  # --- the payload: distrocheck plus every source file, at the paths above ---
  payload = pkgs.runCommand "mayfly-payload" { } (''
    mkdir -p $out
    install -Dm755 ${tools}/bin/distrocheck $out/distrocheck
  '' + lib.concatMapStrings
    (c: ''
      install -D -m${c.mode} ${c.src} "$out/${c.rel}"
    '')
    allCopies);

  # per-distro check list: a check applies to a distro only when its `when` is true
  checksForDistro = d: map (m: m.manifest)
    (lib.filter (m: m.when d) (lib.attrValues checkModels));

  manifest = {
    payload = "${payload}";
    subjects = map (m: m.manifest) subjectModels;
    checks = map (m: m.manifest) (lib.attrValues checkModels); # shared default
    distros = map
      (d: {
        inherit (d) name pm family packages;
        title = d.title or d.name;
        image = "${imageOf d}";
        boot = {
          mem = d.boot.mem or 0;
          cpus = d.boot.cpus or 0;
          disk = d.boot.disk or 0;
        };
        checks = checksForDistro d;
      })
      distroList;
  };
  manifestFile = pkgs.writeText "mayfly-manifest.json" (builtins.toJSON manifest);

  # --- the app: the host runner, wrapped with its tools and a backstop trap ---
  runtime = [ pkgs.qemu pkgs.xorriso pkgs.openssh pkgs.coreutils ];
  # Defaulted options become flags; a user flag after them still wins. The runner
  # bounds concurrency by host CPU and RAM on its own; `-parallel N` caps it, a
  # bare CLI flag for the rare case (nix run .#matrix -- -parallel 2), not an
  # option - the default is always right.
  cfgArgs = lib.concatStringsSep " " (
    lib.optional cfg.keepWorkDir "-keep"
    ++ [
      "-boot-timeout ${cfg.timeouts.boot}"
      "-ssh-timeout ${cfg.timeouts.ssh}"
      "-cloud-init-timeout ${cfg.timeouts.cloudInit}"
    ]
  );
  mkApp = extraArgs: pkgs.writeShellApplication {
    name = "mayfly";
    runtimeInputs = runtime;
    text = ''
      run_dir="$(mktemp -d "''${XDG_RUNTIME_DIR:-/tmp}/mayfly.XXXXXX")"
      export MAYFLY_RUN_DIR="$run_dir"
      # Backstop teardown: if the runner is killed outright, this still kills the
      # VM process group and removes this run's overlays. The runner removes each
      # work dir itself on a clean exit; this is the belt-and-braces layer.
      trap 'kill -- -$$ 2>/dev/null || true; rm -rf "$run_dir" 2>/dev/null || true' EXIT INT TERM
      exec ${tools}/bin/matrix -manifest ${manifestFile} ${cfgArgs} ${extraArgs} "$@"
    '';
  };

  perDistroApps = lib.listToAttrs (map
    (d: {
      name = "distro-${d.name}";
      value = { type = "app"; program = lib.getExe (mkApp "-distro ${d.name}"); };
    })
    distroList);

  # --- the generated README distro table ---
  tableRows = lib.concatMapStrings
    (d: "| ${d.title or d.name} | `${d.name}` | ${d.pm} | ${d.family or "-"} |\n")
    distroList;
  readmeTable = ''
    <!-- generated by nix run .#readme; do not edit by hand -->
    | Distro | Attr | Package manager | Family |
    | --- | --- | --- | --- |
    ${tableRows}'';
  readme = pkgs.writeText "mayfly-table.md" readmeTable;
in
{
  apps = {
    matrix = { type = "app"; program = lib.getExe (mkApp ""); };
  } // perDistroApps;

  # `checks."distro-<name>"` needs a KVM builder with a relaxed sandbox (see
  # README); on any other builder Nix refuses it with its own message.
  checks = lib.listToAttrs (map
    (d: {
      name = "distro-${d.name}";
      value = pkgs.runCommand "distro-${d.name}-check"
        {
          requiredSystemFeatures = [ "kvm" ];
          __noChroot = true;
          nativeBuildInputs = runtime;
        } ''
        export MAYFLY_RUN_DIR="$(mktemp -d)"
        ${tools}/bin/matrix -manifest ${manifestFile} -distro ${d.name} | tee "$out"
      '';
    })
    distroList);

  inherit readme manifest manifestFile;
  readmeText = readmeTable;
}

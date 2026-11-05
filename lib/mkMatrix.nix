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

  # The simple tier (`test`) vs the plan (`entrypoint` + `checks`). Guarded so a
  # consumer picks one, and xfail is rejected until mayfly/1 implements it (spec
  # 6.2.3). checkConfig is forced by mkManifest so these throws reach the user.
  isSugar = cfg.test != null;
  entrypointSrc = if isSugar then cfg.test else cfg.entrypoint;
  xfailSet = lib.filterAttrs (_: c: c.xfail) cfg.checks;
  checkConfig =
    lib.throwIf (isSugar && (cfg.entrypoint != null || cfg.checks != { }))
      "mayfly: set `test` (simple tier) OR `entrypoint` + `checks` (the plan), not both"
      (lib.throwIf (!isSugar && cfg.entrypoint == null)
        "mayfly: set `test` (simple tier), or `entrypoint` + `checks` (the plan)"
        (lib.throwIf (xfailSet != { })
          "mayfly: xfail is not implemented in mayfly/1 (spec 6.2.3); remove it from: ${lib.concatStringsSep ", " (lib.attrNames xfailSet)}"
          null));

  # Plan entries (spec 4.2). `test` sugar is the single reserved id; otherwise one
  # entry per `checks` attr, in attr order, filterable per distro by `when`.
  planEntry = id: c: { inherit id; xfail = c.xfail; };
  fullPlan =
    if isSugar then [ { id = "test"; xfail = false; } ]
    else lib.mapAttrsToList planEntry cfg.checks;
  planForDistro = d:
    if isSugar then [ { id = "test"; xfail = false; } ]
    else lib.mapAttrsToList planEntry (lib.filterAttrs (_: c: c.when d) cfg.checks);

  imageOf = d:
    if d.image ? drv then d.image.drv pkgs
    else
      pkgs.fetchurl ({ inherit (d.image) url; }
        // (if d.image ? sha512 then { inherit (d.image) sha512; } else { inherit (d.image) sha256; }));

  # --- normalize the consumer's subjects ---
  subjectSet = (lib.optionalAttrs (subject != null) { ${subject.name or "subject"} = subject; }) // subjects;

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
      };
    };

  subjectModels = lib.mapAttrsToList subjectModel subjectSet;

  # The consumer's one entrypoint, staged at a fixed payload path.
  entrypointCopy = { rel = "entrypoint"; src = entrypointSrc; mode = "0755"; };

  allCopies = lib.concatMap (m: m.copies) subjectModels ++ [ entrypointCopy ];

  # --- the payload: distrocheck plus every source file, at the paths above ---
  payload = pkgs.runCommand "mayfly-payload" { } (''
    mkdir -p $out
    install -Dm755 ${tools}/bin/distrocheck $out/distrocheck
  '' + lib.concatMapStrings
    (c: ''
      install -D -m${c.mode} ${c.src} "$out/${c.rel}"
    '')
    allCopies);

  # A manifest over an explicit distro list. Only the images of the distros it
  # names are interpolated, so building it realizes only those images -- a
  # per-distro manifest fetches one image, not the whole catalog. That keeps a
  # single-distro run (`nix run .#distro-ubuntu-2404`, the CI smoke) cheap and,
  # crucially, independent of every other distro's image: one stale catalog URL
  # can no longer break an unrelated distro's run. checkConfig is forced here so
  # the config guards (test-xor-plan, xfail) reach the user.
  mkManifest = distros: builtins.seq checkConfig {
    payload = "${payload}";
    subjects = map (m: m.manifest) subjectModels;
    entrypoint = "entrypoint";
    plan = fullPlan;
    test_sugar = isSugar;
    test_user = cfg.testUser;
    audit = cfg.audit;
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
        plan = planForDistro d;
      })
      distros;
  };
  mkManifestFile = distros: pkgs.writeText "mayfly-manifest.json" (builtins.toJSON (mkManifest distros));

  # The full matrix manifest (every enabled distro); per-distro apps/checks below
  # use a one-distro manifest instead.
  manifest = mkManifest distroList;
  manifestFile = mkManifestFile distroList;

  # --- the app: the host runner, wrapped with its tools and a backstop trap ---
  runtime = [ pkgs.qemu pkgs.xorriso pkgs.openssh pkgs.coreutils ];
  # Defaulted options become flags; a user flag after them still wins. The engine
  # takes `-parallel N` (0 = host-bounded, N = that many at once, 1 = sequential).
  # The module's `concurrency` maps onto it: "auto" omits the flag (host-bounded),
  # an integer is passed through.
  concurrencyArg =
    if cfg.concurrency == "auto" then [ ]
    else [ "-parallel ${toString cfg.concurrency}" ];
  cfgArgs = lib.concatStringsSep " " (
    concurrencyArg
    ++ lib.optional cfg.keepWorkDir "-keep"
    ++ [
      "-boot-timeout ${cfg.timeouts.boot}"
      "-ssh-timeout ${cfg.timeouts.ssh}"
      "-cloud-init-timeout ${cfg.timeouts.cloudInit}"
    ]
  );
  mkApp = mf: extraArgs: pkgs.writeShellApplication {
    name = "mayfly";
    runtimeInputs = runtime;
    text = ''
      run_dir="$(mktemp -d "''${XDG_RUNTIME_DIR:-/tmp}/mayfly.XXXXXX")"
      export MAYFLY_RUN_DIR="$run_dir"
      # Backstop teardown: if the runner is killed outright, this still kills the
      # VM process group and removes this run's overlays. The runner removes each
      # work dir itself on a clean exit; this is the belt-and-braces layer.
      trap 'kill -- -$$ 2>/dev/null || true; rm -rf "$run_dir" 2>/dev/null || true' EXIT INT TERM
      exec ${tools}/bin/matrix -manifest ${mf} ${cfgArgs} ${extraArgs} "$@"
    '';
  };

  # Each per-distro app carries a manifest naming only its own distro, so running
  # it fetches that one image -- not the whole catalog.
  perDistroApps = lib.listToAttrs (map
    (d: {
      name = "distro-${d.name}";
      value = { type = "app"; program = lib.getExe (mkApp (mkManifestFile [ d ]) "-distro ${d.name}"); };
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
    matrix = { type = "app"; program = lib.getExe (mkApp manifestFile ""); };
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
        ${tools}/bin/matrix -manifest ${mkManifestFile [ d ]} -distro ${d.name} | tee "$out"
      '';
    })
    distroList);

  inherit readme manifest manifestFile;
  readmeText = readmeTable;
}

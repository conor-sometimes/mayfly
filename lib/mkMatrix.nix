# mkMatrix: the one function that turns a declarative description into a running
# matrix. A consumer flips distros on from the catalog, points at a subject and
# a test, and gets back apps and checks to splice into its own flake.
#
#   distro-matrix.lib.mkMatrix {
#     inherit pkgs;
#     distros = { ubuntu-2404.enable = true; };
#     subject = { binary = "${pkg}/bin/foo"; unit = ./foo.service; };
#     test = ./check.sh;                 # exit 0 is a pass
#   }
#
# matrixSrc and catalogSet are baked in by the flake; a consumer passes only the
# declarative bits.
{ pkgs
, lib ? pkgs.lib
, matrixSrc
, catalogSet
, # --- the consumer's declaration ---
  distros ? { }
, subject ? null
, subjects ? { }
, test ? null
, checks ? { }
}:
let
  # --- resolve distros: catalog-enable plus any custom entry ---
  enabledNames = lib.filterAttrs (name: _: (distros.${name} or { }).enable or false) catalogSet;
  fromCatalog = lib.mapAttrs
    (name: entry: entry // (removeAttrs (distros.${name} or { }) [ "enable" ]) // { inherit name; })
    enabledNames;
  custom = lib.mapAttrs (name: v: v // { inherit name; })
    (lib.filterAttrs (name: v: !(catalogSet ? ${name}) && (v ? image)) distros);
  resolved = fromCatalog // custom;
  distroList = lib.attrValues resolved;

  imageOf = d:
    if d.image ? drv then d.image.drv pkgs
    else pkgs.fetchurl ({ inherit (d.image) url; }
      // (if d.image ? sha512 then { inherit (d.image) sha512; } else { inherit (d.image) sha256; }));

  # --- normalize the consumer's subjects and checks ---
  subjectSet = (lib.optionalAttrs (subject != null) { ${subject.name or "subject"} = subject; }) // subjects;
  checkSet = (lib.optionalAttrs (test != null) { test = { kind = "exit"; run = test; }; }) // checks;

  # --- the Go binaries: distrocheck (guest) and matrix (host), static ---
  tools = pkgs.buildGoModule {
    pname = "distro-matrix";
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
      unitFiles = (s.units or [ ]) ++ (lib.optional (s ? unit) s.unit);
      units = lib.imap0 (i: u: { rel = "subjects/${name}/unit${toString i}-${baseNameOf u}"; src = u; }) unitFiles;
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
        enable = s.enable or (map (u: baseNameOf u) unitFiles);
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
  payload = pkgs.runCommand "distro-matrix-payload" { } (''
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
  manifestFile = pkgs.writeText "distro-matrix-manifest.json" (builtins.toJSON manifest);

  # --- the app: the host runner, wrapped with its tools and a backstop trap ---
  runtime = [ pkgs.qemu pkgs.xorriso pkgs.openssh pkgs.coreutils ];
  mkApp = extraArgs: pkgs.writeShellApplication {
    name = "distro-matrix";
    runtimeInputs = runtime;
    text = ''
      run_dir="$(mktemp -d "''${XDG_RUNTIME_DIR:-/tmp}/distro-matrix.XXXXXX")"
      export DISTRO_MATRIX_RUN_DIR="$run_dir"
      # Backstop teardown: if the runner is killed outright, this still kills the
      # VM process group and removes this run's overlays. The runner removes each
      # work dir itself on a clean exit; this is the belt-and-braces layer.
      trap 'kill -- -$$ 2>/dev/null || true; rm -rf "$run_dir" 2>/dev/null || true' EXIT INT TERM
      exec ${tools}/bin/matrix -manifest ${manifestFile} ${extraArgs} "$@"
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
  readme = pkgs.writeText "distro-matrix-table.md" readmeTable;
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
        export DISTRO_MATRIX_RUN_DIR="$(mktemp -d)"
        ${tools}/bin/matrix -manifest ${manifestFile} -distro ${d.name} | tee "$out"
      '';
    })
    distroList);

  inherit readme manifest;
  readmeText = readmeTable;
}

{
  description = "mayfly: boot N real Linux distros under KVM, run your checks on each, tear every VM down";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      lib = nixpkgs.lib;

      catalogMod = import ./distros/catalog.nix { inherit lib; };
      catalogSet = catalogMod.catalog;
      dmlib = import ./lib { inherit lib catalogSet; matrixSrc = self; };

      # The host runner and in-guest oracle, built once for the default app.
      toolsPkg = pkgs.buildGoModule {
        pname = "mayfly";
        version = "0.1.0";
        src = self;
        vendorHash = null;
        env.CGO_ENABLED = 0;
        subPackages = [ "cmd/distrocheck" "cmd/matrix" ];
        doCheck = false;
      };

      # The full catalog as a JSON manifest with store images, baked into the
      # default app so the CLI-flag path can name any catalog distro.
      imageOf = d:
        pkgs.fetchurl ({ inherit (d.image) url; }
          // (if d.image ? sha512 then { inherit (d.image) sha512; } else { inherit (d.image) sha256; }));
      catalogManifest = pkgs.writeText "mayfly-catalog.json" (builtins.toJSON
        (lib.mapAttrsToList
          (name: d: {
            inherit name;
            title = d.title or name;
            inherit (d) pm family packages;
            image = "${imageOf d}";
            boot = { mem = 0; cpus = 0; disk = 0; };
            checks = [ ];
          })
          catalogSet));

      # The headline app. With no args it reads ./mayfly.nix (a mayfly module),
      # evaluates it to a manifest in the store, and runs that. With flags, it is
      # the no-Nix one-off path (a binary and a test). `nix run .#mayfly` and
      # `nix run github:conor-sometimes/mayfly` both land here.
      defaultApp = pkgs.writeShellApplication {
        name = "mayfly";
        runtimeInputs = [ pkgs.qemu pkgs.xorriso pkgs.openssh pkgs.coreutils pkgs.nix ];
        text = ''
          run_dir="$(mktemp -d "''${XDG_RUNTIME_DIR:-/tmp}/mayfly.XXXXXX")"
          export MAYFLY_RUN_DIR="$run_dir"
          trap 'kill -- -$$ 2>/dev/null || true; rm -rf "$run_dir" 2>/dev/null || true' EXIT INT TERM

          if [ "$#" -gt 0 ] && [ "''${1#-}" != "$1" ]; then
            # The first argument is a flag: the no-Nix path (a binary and a test).
            exec ${toolsPkg}/bin/matrix \
              -catalog ${catalogManifest} \
              -distrocheck ${toolsPkg}/bin/distrocheck "$@"
          fi

          if [ ! -f mayfly.nix ]; then
            echo "mayfly: no ./mayfly.nix here, and no flags." >&2
            echo "Write a mayfly.nix (see the README) or pass -binary/-test for the no-Nix path." >&2
            exit 2
          fi

          # Evaluate ./mayfly.nix (a mayfly module) to a run manifest in the store.
          mf="$(nix --extra-experimental-features 'nix-command flakes' build --impure --no-link --print-out-paths --expr '
            let
              mayfly = builtins.getFlake "'"${self}"'";
              pkgs = import mayfly.inputs.nixpkgs { system = builtins.currentSystem; };
            in (mayfly.lib.buildFromModules {
              inherit pkgs;
              modules = [ (/. + (builtins.getEnv "PWD" + "/mayfly.nix")) ];
              specialArgs = { inherit mayfly; };
            }).manifestFile')"
          exec ${toolsPkg}/bin/matrix -manifest "$mf" "$@"
        '';
      };

      # The smoke subject: a tiny static hello, built from this repo.
      helloPkg = pkgs.buildGoModule {
        pname = "mayfly-hello";
        version = "0.1.0";
        src = self;
        vendorHash = null;
        env.CGO_ENABLED = 0;
        subPackages = [ "cmd/hello" ];
        doCheck = false;
      };

      # The smoke matrix: hello installed and run on the enabled distros. This is
      # the repo's own `nix run .#matrix` and `nix run .#distro-<name>`.
      smoke = dmlib.mkMatrix {
        inherit pkgs;
        distros = { ubuntu-2404.enable = true; };
        subjects.hello = { binary = "${helloPkg}/bin/hello"; };
        test = ./smoke/smoke.sh;
      };

      # The demo: zero input. Boots ubuntu-2404, installs a package, drops the
      # tiny hello subject, runs a check, prints the green matrix, tears down.
      # `nix run github:conor-sometimes/mayfly#demo`.
      demo = dmlib.mkMatrix {
        inherit pkgs;
        distros = { ubuntu-2404.enable = true; };
        subjects.hello = { binary = "${helloPkg}/bin/hello"; packages = [ "curl" ]; };
        test = ./smoke/smoke.sh;
      };
      demoApp = pkgs.writeShellApplication {
        name = "mayfly-demo";
        text = ''
          echo "mayfly demo: booting Ubuntu 24.04, installing a package, running a check, tearing down."
          echo "(needs KVM; one command, zero setup)"
          echo
          exec ${demo.apps.matrix.program} "$@"
        '';
      };

      # The oracle's unit tests, run in the Nix sandbox (pure, no network, no KVM).
      goTest = pkgs.runCommand "mayfly-gotest"
        { nativeBuildInputs = [ pkgs.go ]; } ''
        export HOME="$TMPDIR" GOCACHE="$TMPDIR/gocache" GOFLAGS=-mod=mod GOPROXY=off
        cp -r ${self} src && chmod -R u+w src && cd src
        go test ./internal/... | tee "$out"
      '';

      # The generated distro table: every entry in the shipped catalog, one row.
      catalogList = lib.sort (a: b: a.name < b.name)
        (lib.mapAttrsToList (name: d: d // { inherit name; }) catalogSet);
      tableRows = lib.concatMapStrings
        (d: "| ${d.title or d.name} | `${d.name}` | ${d.pm} | ${d.family} |\n")
        catalogList;
      catalogTable = pkgs.writeText "mayfly-table.md" ''
        <!-- generated by `nix run .#readme`; do not edit by hand -->
        | Distro | Attr | Package manager | Family |
        | --- | --- | --- | --- |
        ${tableRows}'';

      # The committed table must match the catalog: a new distro attr with a
      # stale distros/TABLE.md fails CI.
      readmeCheck = pkgs.runCommand "mayfly-readme-check" { } ''
        if ! diff -u ${./distros/TABLE.md} ${catalogTable}; then
          echo "distros/TABLE.md is stale; run: nix run .#readme > distros/TABLE.md"
          exit 1
        fi
        touch "$out"
      '';

      readmeApp = pkgs.writeShellApplication {
        name = "mayfly-readme";
        text = "cat ${catalogTable}";
      };

      # The options reference, generated from the schema (lib/module.nix), so it
      # never drifts. `nix build .#options-doc` writes the Markdown.
      optionsDoc = (pkgs.nixosOptionsDoc {
        options = (dmlib.evalCfg { inherit pkgs; modules = [ ]; }).options.mayfly;
      }).optionsCommonMark;
    in
    {
      lib = { inherit (dmlib) mkMatrix buildFromModules fetchRelease mayflyModule evalCfg; };

      # The shipped catalog, for `mayfly.distros` and lib.filterAttrs over it.
      distros = catalogSet;

      packages.${system} = {
        default = helloPkg;
        hello = helloPkg;
        readme = catalogTable;
        options-doc = optionsDoc;
      };

      apps.${system} = smoke.apps // {
        default = { type = "app"; program = lib.getExe defaultApp; };
        mayfly = { type = "app"; program = lib.getExe defaultApp; };
        demo = { type = "app"; program = lib.getExe demoApp; };
        readme = { type = "app"; program = lib.getExe readmeApp; };
      };

      # `nix flake check`: the pure checks only. The per-distro VM runs need KVM
      # and a relaxed sandbox (README); they are gated by `nix run .#distro-<name>`
      # on a KVM host, and mkMatrix returns them for consumers who want them.
      checks.${system} = {
        gotest = goTest;
        readme = readmeCheck;
      };

      formatter.${system} = pkgs.nixpkgs-fmt;
    };
}

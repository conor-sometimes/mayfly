# The repo's library. A mayfly run is a module: lib/module.nix declares the
# options, evalModules type-checks the user's config, and lib/mkMatrix.nix builds
# the matrix from it. matrixSrc and catalogSet are baked in by the flake.
{ lib, matrixSrc, catalogSet }:
let
  evalCfg = { pkgs, modules, specialArgs ? { } }:
    lib.evalModules {
      specialArgs = { inherit catalogSet pkgs; } // specialArgs;
      modules = [ ./module.nix ] ++ modules;
    };

  build = pkgs: eval:
    (import ./mkMatrix.nix {
      inherit pkgs lib matrixSrc catalogSet;
      cfg = eval.config.mayfly;
    }) // { inherit eval; };
in
rec {
  # buildFromModules { pkgs; modules; } evaluates mayfly modules (a mayfly.nix is
  # one such module) and builds the matrix. The default app uses it on the cwd.
  buildFromModules = { pkgs, modules, specialArgs ? { } }:
    build pkgs (evalCfg { inherit pkgs modules specialArgs; });

  # mkMatrix { pkgs; distros; subject; test; checks; ... } -> { apps; checks;
  # readme; manifestFile; eval; }. The attrset is sugar for a one-module run.
  mkMatrix = { pkgs, ... }@args:
    buildFromModules {
      inherit pkgs;
      modules = [ { mayfly = removeAttrs args [ "pkgs" "lib" ]; } ];
    };

  # The options schema, for a consumer that wants to set mayfly.* in their own
  # module system, and for generating the options reference.
  mayflyModule = ./module.nix;
  inherit evalCfg;

  # fetchRelease unpacks a project's own release archive and returns the path of
  # one binary inside it, so an example tests the bytes the project ships.
  #
  #   lib.fetchRelease pkgs { url = "...tar.gz"; sha256 = "..."; bin = "caddy"; }
  fetchRelease = pkgs: { url, sha256, bin }:
    let src = pkgs.fetchurl { inherit url sha256; };
    in
    pkgs.runCommand "release-${bin}" { nativeBuildInputs = [ pkgs.gnutar pkgs.gzip pkgs.xz pkgs.unzip ]; } ''
      mkdir unpack && cd unpack
      case "${url}" in
        *.tar.gz|*.tgz) tar xzf ${src} ;;
        *.tar.xz)       tar xJf ${src} ;;
        *.tar)          tar xf  ${src} ;;
        *.zip)          unzip -q ${src} ;;
        *)              cp ${src} ./${bin} ;;
      esac
      found="$(find . -type f -name '${bin}' -perm -u+x | head -1)"
      [ -z "$found" ] && found="$(find . -type f -name '${bin}' | head -1)"
      if [ -z "$found" ]; then echo "no '${bin}' in the release archive"; exit 1; fi
      install -Dm755 "$found" "$out/bin/${bin}"
    '';
}

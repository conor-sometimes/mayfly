# The repo's library: mkMatrix (the engine) and fetchRelease (an upstream static
# release by hash, for the worked examples). matrixSrc and catalogSet are baked
# in by the flake so a consumer calls mkMatrix with only its own declaration.
{ lib, matrixSrc, catalogSet }:
{
  # mkMatrix { pkgs; distros; subject|subjects; test|checks; } -> { apps; checks; readme; }
  mkMatrix = args: import ./mkMatrix.nix ({ inherit lib matrixSrc catalogSet; } // args);

  # fetchRelease unpacks a project's own release archive and returns the path of
  # one binary inside it, so an example tests the bytes the project ships, not a
  # nixpkgs rebuild. The matrix's static assertion still runs on it.
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

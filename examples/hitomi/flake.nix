{
  description = "hitomi on the mayfly matrix: the shipped YARA scanner's accuracy on real distros";

  inputs.mayfly.url = "github:conor-sometimes/mayfly";
  inputs.hitomi.url = "github:conor-sometimes/hitomi";
  inputs.hitomi.inputs.nixpkgs.follows = "mayfly/nixpkgs";

  outputs = { self, mayfly, hitomi }:
    let
      system = "x86_64-linux";
      pkgs = mayfly.inputs.nixpkgs.legacyPackages.${system};
      matrix = mayfly.lib.buildFromModules {
        inherit pkgs;
        modules = [ ./mayfly.nix ];
        specialArgs = { inherit hitomi; };
      };
    in
    {
      apps.${system} = matrix.apps;
    };
}

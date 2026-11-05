{
  description = "mayfly example: the result protocol, exercised by an entrypoint";

  inputs.mayfly.url = "github:conor-sometimes/mayfly";

  outputs = { self, mayfly }:
    let
      system = "x86_64-linux";
      pkgs = mayfly.inputs.nixpkgs.legacyPackages.${system};
      matrix = mayfly.lib.buildFromModules {
        inherit pkgs;
        modules = [ ./mayfly.nix ];
        specialArgs = { inherit mayfly; };
      };
    in
    {
      apps.${system} = matrix.apps;
    };
}

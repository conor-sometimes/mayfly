{
  description = "mimi on the mayfly matrix: the eBPF process-telemetry sensor attaches and emits on a real Ubuntu kernel";

  inputs.mayfly.url = "github:conor-sometimes/mayfly";
  inputs.mimi.url = "github:conor-sometimes/mimi";
  inputs.mimi.inputs.nixpkgs.follows = "mayfly/nixpkgs";

  outputs = { self, mayfly, mimi }:
    let
      system = "x86_64-linux";
      pkgs = mayfly.inputs.nixpkgs.legacyPackages.${system};
      matrix = mayfly.lib.buildFromModules {
        inherit pkgs;
        modules = [ ./mayfly.nix ];
        specialArgs = { inherit mimi; };
      };
    in
    {
      apps.${system} = matrix.apps;
    };
}

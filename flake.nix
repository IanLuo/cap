{
  description = "cap — deterministic watcher and advisor over the fs board";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "aarch64-darwin" "x86_64-darwin" "aarch64-linux" "x86_64-linux" ];
      eachSystem = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      perSystem = f: nixpkgs.lib.genAttrs systems (system: f system nixpkgs.legacyPackages.${system});
    in {
      # Dependencies are vendored and committed (vendorHash = null): the build
      # delegated to the nix daemon cannot reach a module proxy. If go.mod
      # changes, regenerate with:
      #   nix develop -c sh -c 'GOPROXY=off GOFLAGS=-mod=mod go mod vendor'
      # then commit the tree — nix copies only tracked files.
      packages = perSystem (system: pkgs: {
        default = (pkgs.buildGoModule.override { go = pkgs.go_1_27; }) {
          pname = "cap";
          version = "0.1.0";
          src = ./.;
          vendorHash = null;
          subPackages = [ "cmd/cap" ];
          meta = {
            description = "Deterministic watcher and advisor over the fs board";
            mainProgram = "cap";
          };
        };
      });

      devShells = eachSystem (pkgs: {
        default = pkgs.mkShell { packages = [ pkgs.go_1_27 pkgs.gopls ]; };
      });
    };
}

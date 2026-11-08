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

      # The watcher's supervisor. Import this from a home-manager config; the
      # module owns one launchd agent and nothing else. `fs` is resolved from
      # the profile PATH unless fsPackage names a package, so the flake does not
      # depend on another repo's flake.
      homeManagerModules.default = { config, lib, pkgs, ... }:
        let
          cfg = config.programs.cap;
          system = pkgs.stdenv.hostPlatform.system;
        in {
          options.programs.cap = {
            enable = lib.mkEnableOption "the cap watcher under launchd";
            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${system}.default;
              description = "The cap package to run.";
            };
            fsPackage = lib.mkOption {
              type = lib.types.nullOr lib.types.package;
              default = null;
              description = "Package providing fs; null uses the profile PATH.";
            };
            trace = lib.mkOption {
              type = lib.types.str;
              default = "${config.home.homeDirectory}/.cap/trace.log";
              description = "Where the watcher writes its trace.";
            };
          };

          config = lib.mkIf cfg.enable {
            home.packages = [ cfg.package ];

            launchd.agents.cap = {
              enable = true;
              config = {
                Label = "home-manager.cap";
                ProgramArguments = [ "${cfg.package}/bin/cap" "watch" ];
                KeepAlive = true;
                EnvironmentVariables = {
                  CAP_TRACE = cfg.trace;
                  PATH = lib.concatStringsSep ":" (
                    [ "${config.home.profileDirectory}/bin" ]
                    ++ lib.optionals (cfg.fsPackage != null) [ "${cfg.fsPackage}/bin" ]
                    ++ [ "/run/current-system/sw/bin" "/usr/bin" "/bin" "/usr/sbin" "/sbin" ]
                  );
                };
                StandardOutPath = "${config.home.homeDirectory}/Library/Logs/cap.out.log";
                StandardErrorPath = "${config.home.homeDirectory}/Library/Logs/cap.err.log";
              };
            };
          };
        };
    };
}

{
  description = "LOOM NixOS runtime profiles";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";
    # Hermes v0.21.0 / v2026.8.31, peeled annotated tag. Keep its own
    # dependency lock: its Python/Node toolchain is independent of LOOM's.
    hermes-upstream.url = "github:NousResearch/hermes-agent/29112bef099274229cadff79cdff7bf7b99c4b77";
    # Basecamp v0.10.0; retain its separate Go 1.26 toolchain lock.
    basecamp-upstream.url = "github:basecamp/basecamp-cli/e4bfd014bf137771d454515b4f7314ce651ee096";
  };

  outputs =
    { self, nixpkgs, hermes-upstream, basecamp-upstream }:
    let
      lib = nixpkgs.lib;

      mkNixos =
        { system, modules }:
        lib.nixosSystem {
          inherit system;
          specialArgs = {
            inherit self;
          };
          modules = modules;
        };

      devShellSystems = [
        "aarch64-darwin"
        "x86_64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];

      forEachSystem =
        f:
        lib.genAttrs devShellSystems (
          system:
          f {
            inherit system;
            pkgs = import nixpkgs { inherit system; };
          }
        );

      mkGoPackage =
        {
          pkgs,
          name,
          subPackages,
          vendorHash ? "sha256-Q8RWoTCPXWxSnEapfYjel9Q+sFlWYp653w17I2R+jdw=",
        }:
        pkgs.buildGoModule {
          pname = name;
          version = "0.0.0-dev";
          src = ./.;
          inherit subPackages;
          inherit vendorHash;

          ldflags = [
            "-s"
            "-w"
            "-X loom.local/loom/internal/version.Version=0.0.0-dev"
          ];

          postInstall = ''
            mkdir -p $out/share/loom/migrations
            cp -r migrations/*.sql $out/share/loom/migrations/
            mkdir -p $out/share/loom/docs
            cp -r docs/. $out/share/loom/docs/
            mkdir -p $out/share/loom/ai-loom-pack
            cp -r ai-loom-pack/. $out/share/loom/ai-loom-pack/
          '';
        };
    in
    {
      packages = forEachSystem (
        { system, pkgs }:
        ({
          mina-matrix-teal = pkgs.writeText "mina-matrix-teal.yaml" (builtins.readFile ./nix/files/mina/skins/mina-matrix-teal.yaml);

          basecamp-cli = pkgs.callPackage ./nix/packages/basecamp-cli.nix {
            inherit basecamp-upstream system;
          };

          loomd = mkGoPackage {
            inherit pkgs;
            name = "loomd";
            subPackages = [
              "cmd/loomd"
            ];
          };

          loom = mkGoPackage {
            inherit pkgs;
            name = "loom";
            subPackages = [
              "cmd/loom"
            ];
          };

          loom-node-agent = mkGoPackage {
            inherit pkgs;
            name = "loom-node-agent";
            subPackages = [
              "cmd/loom-node-agent"
            ];
          };

          loom-mini-dashboard = mkGoPackage {
            inherit pkgs;
            name = "loom-mini-dashboard";
            subPackages = [
              "cmd/loom-mini-dashboard"
            ];
          };

          loom-service-manager = mkGoPackage {
            inherit pkgs;
            name = "loom-service-manager";
            subPackages = [ "cmd/loom-service-manager" ];
            vendorHash = "sha256-Q8RWoTCPXWxSnEapfYjel9Q+sFlWYp653w17I2R+jdw=";
          };

          loom-project-preparation = mkGoPackage {
            inherit pkgs;
            name = "loom-project-preparation";
            subPackages = [ "cmd/loom-project-preparation" ];
          };

          loom-service-fixture = mkGoPackage {
            inherit pkgs;
            name = "loom-service-fixture";
            subPackages = [ "cmd/loom-service-fixture" ];
          };

          loom-restore-authority = mkGoPackage {
            inherit pkgs;
            name = "loom-restore-authority";
            subPackages = [ "cmd/loom-restore-authority" ];
          };

          default = self.packages.${system}.loomd;
        } // lib.optionalAttrs (lib.elem system [ "aarch64-darwin" "aarch64-linux" "x86_64-linux" ]) {
          hermes-agent = pkgs.callPackage ./nix/packages/hermes-agent.nix {
            inherit hermes-upstream system;
          };
          mac-computer-use-bridge = import ./nix/packages/mac-computer-use-bridge.nix {
            inherit hermes-upstream system;
          };
        } // lib.optionalAttrs (system == "x86_64-linux") {
          agent-browser = pkgs.callPackage ./nix/packages/agent-browser.nix {
            chromium = hermes-upstream.inputs.nixpkgs.legacyPackages.${system}.chromium;
          };
          orca-headless = pkgs.callPackage ./nix/packages/orca-headless.nix { };
          proton-pass-cli = pkgs.callPackage ./nix/packages/proton-pass-cli.nix { };
        })
      );

      checks.x86_64-linux.orca-folder-bootstrap = import ./nix/checks/orca-folder-bootstrap.nix {
        pkgs = nixpkgs.legacyPackages.x86_64-linux;
        orca = self.packages.x86_64-linux.orca-headless;
      };

      nixosModules.loom-orca = import ./nix/modules/loom-orca.nix;
      checks.aarch64-linux.project-applications-rendered = import ./nix/tests/project-applications.nix {
        inherit lib self;
        pkgs = nixpkgs.legacyPackages.aarch64-linux;
      };
      checks.x86_64-linux.project-applications-rendered = import ./nix/tests/project-applications.nix {
        inherit lib self;
        pkgs = nixpkgs.legacyPackages.x86_64-linux;
      };

      nixosModules.loom-project-preparation = import ./nix/modules/loom-project-preparation.nix;
      checks.aarch64-linux.project-preparation-rendered = import ./nix/tests/project-preparation.nix {
        inherit lib self;
        pkgs = nixpkgs.legacyPackages.aarch64-linux;
      };
      checks.x86_64-linux.project-preparation-rendered = import ./nix/tests/project-preparation.nix {
        inherit lib self;
        pkgs = nixpkgs.legacyPackages.x86_64-linux;
      };

      lib.projectApplicationArtifact = import ./nix/lib/project-application-artifact.nix { inherit lib; };
      nixosModules.loom-project-applications = import ./nix/modules/loom-project-applications.nix;
      nixosModules.loom-morathustra = import ./nix/modules/loom-morathustra.nix;
      # Same module and one selected runtime, not a second gateway definition.
      nixosModules.loom-mina = import ./nix/modules/loom-morathustra.nix;
      nixosModules.loom-service-manager = import ./nix/modules/loom-service-manager.nix;
      nixosModules.loom-restore-authority = import ./nix/modules/loom-restore-authority.nix;

      nixosConfigurations = {
        dev-utm = mkNixos {
          system = "aarch64-linux";
          modules = [
            ./nix/hosts/dev-utm/configuration.nix
          ];
        };

        production-placeholder = mkNixos {
          system = "x86_64-linux";
          modules = [
            ./nix/hosts/production-placeholder/configuration.nix
          ];
        };

        hardware-main = mkNixos {
          system = "x86_64-linux";
          modules = [
            ./nix/hosts/hardware-main/configuration.nix
          ];
        };
      };

      devShells = forEachSystem (
        { pkgs, ... }:
        {
          default = pkgs.mkShell {
            packages = with pkgs; [
              git
              go
              openssh
              poppler-utils
              rsync
            ];
          };
        }
      );
    };
}

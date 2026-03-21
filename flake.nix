# flake.nix — Gas Town (dgt) Nix flake
#
# Outputs:
#   packages.<system>.town-ctl           — topology actuator CLI
#   packages.<system>.gastown-operator   — Kubernetes operator
#   packages.<system>.default            — alias for town-ctl
#   devShells.<system>.default           — dev shell with go, dolt, kubectl, both binaries
#   overlays.default                     — nixpkgs overlay exposing pkgs.town-ctl + pkgs.gastown-operator
#   nixosModules.default                 — services.dgt NixOS module
#   checks.<system>.*                    — build + smoke-test checks (see nix/checks.nix)
#
# Quick start:
#   nix profile install github:tenev/dgt   # installs town-ctl
#   nix develop                            # drop into dev shell
#   nix flake check                        # run all checks
{
  description = "Gas Town — declarative AI-agent topology actuator";

  inputs = {
    # Use nixpkgs-unstable for the latest Go toolchain (Go 1.25+).
    nixpkgs-unstable.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs = { self, nixpkgs-unstable }:
    let
      # Supported host systems.  Extend as needed.
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];

      # Helper: apply f to every system and collect results into an attrset.
      # Equivalent to flake-utils' eachSystem without the extra dependency.
      forAllSystems = f: nixpkgs-unstable.lib.genAttrs systems f;

      # Helper: instantiate nixpkgs for a given system.
      pkgsFor = system: import nixpkgs-unstable {
        inherit system;
        overlays = [ self.overlays.default ];
      };
    in
    {
      # ── Packages ───────────────────────────────────────────────────────────────

      packages = forAllSystems (system:
        let pkgs = pkgsFor system; in
        {
          inherit (pkgs) town-ctl gastown-operator;
          default = pkgs.town-ctl;
        }
      );

      # ── Dev Shell ──────────────────────────────────────────────────────────────

      devShells = forAllSystems (system:
        let pkgs = pkgsFor system; in
        {
          default = pkgs.mkShell {
            packages = with pkgs; [
              go
              dolt
              kubectl
              town-ctl
              gastown-operator
            ];

            shellHook = ''
              echo "dgt dev shell — go $(go version | awk '{print $3}')"
            '';
          };
        }
      );

      # ── Overlay ────────────────────────────────────────────────────────────────

      overlays.default = final: _prev: {
        town-ctl = final.callPackage ./nix/packages/town-ctl.nix { };
        gastown-operator = final.callPackage ./nix/packages/operator.nix { };
      };

      # ── NixOS Module ───────────────────────────────────────────────────────────

      nixosModules.default = import ./nix/modules/town-ctl.nix;

      # ── Checks ─────────────────────────────────────────────────────────────────

      checks = forAllSystems (system:
        let pkgs = pkgsFor system; in
        import ./nix/checks.nix {
          inherit (pkgs) town-ctl gastown-operator runCommand;
        }
      );
    };
}

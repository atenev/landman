# nix/checks.nix — build + smoke-test derivations for `nix flake check`
#
# Each attribute becomes checks.<system>.<name>.
# Run all checks locally with:  nix flake check
{ town-ctl, gastown-operator, runCommand }:

{
  # Verify town-ctl builds without errors.
  town-ctl-build = town-ctl;

  # Verify gastown-operator builds without errors.
  operator-build = gastown-operator;

  # Smoke test: town-ctl version must exit 0.
  # (town-ctl uses a custom CLI; --help is not a top-level flag.)
  town-ctl-smoke = runCommand "town-ctl-smoke" { } ''
    ${town-ctl}/bin/town-ctl version
    touch $out
  '';
}

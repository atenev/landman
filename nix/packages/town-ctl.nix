{ lib, buildGoModule }:

buildGoModule {
  pname = "town-ctl";
  version = "0.1.0";

  src = ../..;

  subPackages = [ "cmd/town-ctl" ];

  # Computed via: nix hash path vendor/
  # Update after every `go mod vendor` run.
  vendorHash = "sha256-b6/De61+zAcf9BaHbORtOvlNaohUDloKjTN3C6OFioQ=";

  meta = with lib; {
    description = "Gas Town topology actuator — reads town.toml and writes desired topology to Dolt";
    homepage = "https://github.com/tenev/dgt";
    # Confirm license before publishing; update to the correct spdxId.
    license = licenses.mit;
    mainProgram = "town-ctl";
    platforms = platforms.linux ++ platforms.darwin;
  };
}

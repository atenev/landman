{ lib, buildGoModule }:

buildGoModule {
  pname = "town-ctl";
  version = "0.1.0";

  src = ../..;

  subPackages = [ "cmd/town-ctl" ];

  # vendor/ is committed to the repo; buildGoModule uses it directly.
  vendorHash = null;

  meta = with lib; {
    description = "Gas Town topology actuator — reads town.toml and writes desired topology to Dolt";
    homepage = "https://github.com/tenev/dgt";
    license = licenses.mit;
    mainProgram = "town-ctl";
    platforms = platforms.linux ++ platforms.darwin;
  };
}

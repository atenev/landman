{ lib, buildGoModule }:

buildGoModule {
  pname = "gastown-operator";
  version = "0.1.0";

  src = ../..;

  subPackages = [ "cmd/operator" ];

  # vendor/ is committed to the repo; buildGoModule uses it directly.
  vendorHash = null;

  meta = with lib; {
    description = "Gas Town Kubernetes operator — reconciles Gas Town CRDs with cluster state";
    homepage = "https://github.com/tenev/dgt";
    # Confirm license before publishing; update to the correct spdxId.
    license = licenses.mit;
    mainProgram = "gastown-operator";
    platforms = platforms.linux;
  };
}

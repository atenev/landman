# NixOS module: services.dgt
#
# Implements the Gas Town topology actuator as a NixOS service.
# This module is standalone-importable without the flake.
#
# Minimal usage:
#   { services.dgt.enable = true; services.dgt.configFile = /etc/gt/town.toml; }
#
# With auto-apply on config change:
#   {
#     services.dgt.enable = true;
#     services.dgt.configFile = /etc/gt/town.toml;
#     services.dgt.autoApply.enable = true;
#     services.dgt.autoApply.environmentFile = /run/secrets/dgt-env;
#   }
{ config, lib, pkgs, ... }:

let
  cfg = config.services.dgt;
in
{
  # ── Option declarations ──────────────────────────────────────────────────────

  options.services.dgt = {
    enable = lib.mkEnableOption "Gas Town topology actuator (town-ctl)";

    package = lib.mkOption {
      type        = lib.types.package;
      default     = pkgs.town-ctl;
      defaultText = lib.literalExpression "pkgs.town-ctl";
      description = ''
        The town-ctl package to use.  Override to pin a specific version or to
        use a locally-built derivation during development.
      '';
    };

    configFile = lib.mkOption {
      type        = lib.types.nullOr lib.types.path;
      default     = null;
      example     = lib.literalExpression "/etc/gt/town.toml";
      description = ''
        Absolute path to the town.toml manifest file.
        Required when <option>services.dgt.enable</option> is true.
      '';
    };

    autoApply = {
      enable = lib.mkEnableOption ''
        automatic re-apply whenever <option>services.dgt.configFile</option>
        is modified.  Creates a <literal>dgt-apply.path</literal> systemd path
        unit that watches the config file and triggers
        <literal>dgt-apply.service</literal> on change.
      '';

      environmentFile = lib.mkOption {
        type        = lib.types.nullOr lib.types.path;
        default     = null;
        example     = lib.literalExpression "/run/secrets/dgt-env";
        description = ''
          Path to a file containing environment variable assignments in
          <literal>KEY=value</literal> format (systemd <literal>EnvironmentFile</literal>
          syntax).  Useful for supplying Dolt credentials without embedding them
          in the Nix store.  Set to <literal>null</literal> to disable.
        '';
      };
    };
  };

  # ── Configuration ────────────────────────────────────────────────────────────

  config = lib.mkIf cfg.enable {
    # Assertion: configFile must be set when the service is enabled.
    assertions = [
      {
        assertion = cfg.configFile != null;
        message   = ''
          services.dgt.enable is true but services.dgt.configFile is null.
          Set services.dgt.configFile to the absolute path of your town.toml.
        '';
      }
    ];

    systemd.services.dgt-apply = {
      description = "Gas Town topology actuator — apply town.toml to Dolt";

      # Run after Dolt is available so town-ctl can connect.
      after  = [ "network.target" "dolt.service" ];
      wants  = [ "dolt.service" ];

      serviceConfig = {
        Type            = "oneshot";
        ExecStart       = "${cfg.package}/bin/town-ctl apply --config ${cfg.configFile}";
        RemainAfterExit = false;
      } // lib.optionalAttrs (cfg.autoApply.environmentFile != null) {
        EnvironmentFile = cfg.autoApply.environmentFile;
      };
    };

    systemd.paths.dgt-apply = lib.mkIf cfg.autoApply.enable {
      description     = "Watch town.toml and trigger dgt-apply.service on change";
      wantedBy        = [ "multi-user.target" ];
      pathConfig      = {
        PathModified = cfg.configFile;
        Unit         = "dgt-apply.service";
      };
    };
  };
}

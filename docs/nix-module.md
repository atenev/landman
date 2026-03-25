# NixOS Module Reference — `services.dgt`

The `services.dgt` NixOS module ships `town-ctl` as a managed systemd service
that applies your `town.toml` to Dolt on demand or automatically whenever the
config file changes.

---

## Options

### `services.dgt.enable`

| | |
|---|---|
| **Type** | `bool` |
| **Default** | `false` |

Enable the Gas Town topology actuator.  When `true`, the `dgt-apply.service`
systemd unit is registered.  `services.dgt.configFile` must also be set.

---

### `services.dgt.package`

| | |
|---|---|
| **Type** | `package` |
| **Default** | `pkgs.town-ctl` |

The `town-ctl` package to use.  Override to pin a specific version or to use a
locally-built derivation:

```nix
services.dgt.package = pkgs.callPackage ./nix/packages/town-ctl.nix { };
```

---

### `services.dgt.configFile`

| | |
|---|---|
| **Type** | `null \| path` |
| **Default** | `null` |
| **Example** | `/etc/gt/town.toml` |

Absolute path to the `town.toml` manifest on the target host.  Required when
`services.dgt.enable` is `true`.

The file must **not** contain literal secret values — use env-var interpolation
(`"${ANTHROPIC_API_KEY}"`) and supply the values via `autoApply.environmentFile`
so they never enter the Nix store.

---

### `services.dgt.autoApply.enable`

| | |
|---|---|
| **Type** | `bool` |
| **Default** | `false` |

When `true`, creates a `dgt-apply.path` systemd path unit that watches
`services.dgt.configFile` for modifications and triggers `dgt-apply.service`
automatically.  Without this, the service only runs when started manually or
at boot via `wantedBy`.

---

### `services.dgt.autoApply.environmentFile`

| | |
|---|---|
| **Type** | `null \| path` |
| **Default** | `null` |
| **Example** | `/run/secrets/dgt-env` |

Path to a file containing `KEY=value` assignments in systemd
[`EnvironmentFile`](https://www.freedesktop.org/software/systemd/man/systemd.exec.html#EnvironmentFile=)
format.  The file is read at service start time; its contents are injected as
environment variables into the `town-ctl apply` process.

Use this to supply `ANTHROPIC_API_KEY` and other secrets without embedding them
in the Nix store or in `town.toml`.

---

## Systemd units created by the module

### `dgt-apply.service`

A **oneshot** unit that runs:

```
town-ctl apply --config <configFile>
```

It starts after `network.target` and `dolt.service`.  If `autoApply.environmentFile`
is set, the file is passed as `EnvironmentFile=` so secrets are available during
the apply run.

Trigger a manual re-apply at any time:

```bash
systemctl start dgt-apply
```

Check the last run's output:

```bash
journalctl -u dgt-apply -n 50
```

### `dgt-apply.path` *(created only when `autoApply.enable = true`)*

A path unit that watches `services.dgt.configFile` via `PathModified=` and
activates `dgt-apply.service` whenever the file changes.  This enables a
GitOps loop: push a commit → deploy the new `town.toml` → the module fires
automatically.

---

## Supplying secrets via `EnvironmentFile`

`town.toml` references secrets as environment-variable placeholders:

```toml
[secrets]
anthropic_api_key = "${ANTHROPIC_API_KEY}"
github_token      = "${GITHUB_TOKEN}"
```

Create an environment file (owned by `root`, mode `0400`) outside the Nix
store — for example managed by
[agenix](https://github.com/ryantm/agenix) or
[sops-nix](https://github.com/Mic92/sops-nix):

```
# /run/secrets/dgt-env
ANTHROPIC_API_KEY=sk-ant-…
GITHUB_TOKEN=ghp_…
DOLT_PASSWORD=…
```

Reference it in your NixOS config:

```nix
services.dgt.autoApply.environmentFile = "/run/secrets/dgt-env";
```

`town-ctl` resolves the placeholders at apply time.  Neither the API key nor
any other secret value is written to Dolt or the Nix store.

---

## Configuration examples

### Flake-based NixOS

Add `dgt` as a flake input and import the module:

```nix
# flake.nix
{
  inputs = {
    nixpkgs.url     = "github:NixOS/nixpkgs/nixpkgs-unstable";
    dgt.url         = "github:tenev/dgt";
    dgt.inputs.nixpkgs-unstable.follows = "nixpkgs";
  };

  outputs = { nixpkgs, dgt, ... }: {
    nixosConfigurations.my-host = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        dgt.nixosModules.default   # <── adds services.dgt options
        ./configuration.nix
      ];
    };
  };
}
```

```nix
# configuration.nix
{ ... }:
{
  services.dgt = {
    enable      = true;
    configFile  = /etc/gt/town.toml;

    autoApply = {
      enable          = true;
      environmentFile = "/run/secrets/dgt-env";
    };
  };

  # Deploy your town.toml via e.g. environment.etc:
  environment.etc."gt/town.toml".source = ./town.toml;
}
```

Bump to the latest release:

```bash
nix flake update dgt
nixos-rebuild switch --flake .#my-host
```

---

### Non-flake NixOS (fetchTarball)

Import the standalone module directly without a flake:

```nix
# configuration.nix
{ config, pkgs, ... }:
let
  dgt = builtins.fetchTarball {
    # Pin to a specific commit for reproducibility.
    url    = "https://github.com/tenev/dgt/archive/<git-rev>.tar.gz";
    sha256 = "<sha256>";
  };
in
{
  imports = [
    "${dgt}/nix/modules/town-ctl.nix"
  ];

  # town-ctl binary: inject it via overlay so pkgs.town-ctl is available.
  nixpkgs.overlays = [
    (import "${dgt}/nix/overlays/default.nix")
  ];

  services.dgt = {
    enable      = true;
    configFile  = /etc/gt/town.toml;

    autoApply = {
      enable          = true;
      environmentFile = "/run/secrets/dgt-env";
    };
  };

  environment.etc."gt/town.toml".source = ./town.toml;
}
```

To upgrade, update the `url` to the new commit rev and recalculate `sha256`:

```bash
nix-prefetch-url --unpack https://github.com/tenev/dgt/archive/<new-rev>.tar.gz
```

Then run `nixos-rebuild switch`.

---

## Upgrade path

### Flake users

```bash
# Update only dgt, leave other inputs unchanged:
nix flake update dgt
nixos-rebuild switch --flake .#my-host
```

### Non-flake users

1. Find the new commit SHA on GitHub or `git ls-remote https://github.com/tenev/dgt`.
2. Update the `url` in `builtins.fetchTarball` to the new rev.
3. Recalculate `sha256` with `nix-prefetch-url --unpack <url>`.
4. Run `nixos-rebuild switch`.

# Bare Metal Deployment

This directory contains systemd unit files for deploying Gas Town components on
bare metal (non-Kubernetes) hosts.

## Units

| File | Binary | Purpose |
|------|--------|---------|
| `systemd/gastown-surveyor@.service` | `claude` | Surveyor topology reconciler (template unit, one per town) |
| `systemd/dgt-observer.service` | `dgt-observer` | Prometheus metrics exporter for topology and Beads state |

## Quick Start: Observer + Surveyor on the Same Host

1. **Install binaries:**

   ```bash
   sudo cp result/bin/dgt-observer /usr/local/bin/dgt-observer
   sudo chmod 755 /usr/local/bin/dgt-observer
   ```

2. **Create the observer system user:**

   ```bash
   sudo useradd --system --no-create-home --shell /usr/sbin/nologin dgt-observer
   ```

3. **Create the environment file:**

   ```bash
   sudo mkdir -p /etc/dgt
   sudo tee /etc/dgt/observer.env <<'EOF'
   DGT_DOLT_DSN=root:@tcp(127.0.0.1:3306)/dgt
   EOF
   sudo chmod 600 /etc/dgt/observer.env
   ```

4. **Install and start the unit:**

   ```bash
   sudo cp deploy/systemd/dgt-observer.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now dgt-observer
   ```

5. **Verify metrics are being exported:**

   ```bash
   curl -s http://localhost:9091/metrics | grep dgt_
   ```

6. **Configure Prometheus** to scrape `:9091` (see unit file comments for the
   scrape config snippet).

## Relationship to the Surveyor

The observer is a passive read-only process. It connects to the same Dolt
instance as the Surveyor but never writes. Both services can run concurrently
without coordination. The observer does not need to know which towns are active;
it reads all rows from the topology tables on every poll.

See `systemd/gastown-surveyor@.service` for Surveyor installation instructions.

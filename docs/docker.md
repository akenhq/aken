# Docker logs

## Why the journald driver

The collector runs unprivileged. Docker's `json-file` logs are root-only,
and access to the Docker socket is root-equivalent. Use the journald driver
to make container logs readable through `systemd-journal` membership.

The collector never touches the Docker socket, never runs as root, and never
joins the `docker` group.

## Per-service configuration

Add the logging driver to the service in your Compose configuration:

```yaml
services:
  api:
    # Keep your existing image and other service settings.
    logging:
      driver: journald
```

Apply the configuration as an operator with Docker access:

```sh
docker compose up -d
```

This recreates the service with the new driver. Plan for the interruption.
`docker logs` keeps working.

## Daemon-wide configuration

To make journald the default for new containers, add `log-driver` to
`/etc/docker/daemon.json`. Preserve any other settings in that file:

```json
{
  "log-driver": "journald"
}
```

Restart Docker as root:

```sh
systemctl restart docker
```

The default applies to new containers only. Recreate existing containers to
change their logging driver.

## Check it

Replace `<name>` with the container name. Check journal access as `aken`:

```sh
sudo -u aken journalctl CONTAINER_NAME=<name> -n 5
```

Then check collection and redaction:

```sh
sudo -u aken aken collect --container <name> --dry-run
```

See [Collect logs](collect.md) for time windows and the review screen.

## Rate limiting

All containers share the `docker.service` journald bucket. The default is
10000 messages per 30 s. A busy container can cause messages from other
containers to be dropped too.

To increase the burst limit, create
`/etc/systemd/system/docker.service.d/journal.conf` as root, creating its
parent directory if needed:

```ini
[Service]
LogRateLimitIntervalSec=30
LogRateLimitBurst=100000
```

Reload the configuration and restart Docker as root:

```sh
systemctl daemon-reload
systemctl restart docker
```

## Persistent journal

Ensure `/var/log/journal` exists or set `Storage=persistent` under `[Journal]`
in `/etc/systemd/journald.conf`. Without persistent storage, logs vanish at
reboot and `--since` windows can come back empty.

## Not supported

The collector does not read `/var/lib/docker/containers` or use the Docker
socket or a socket proxy. A read-only socket proxy route is planned for a
later release.

# Updating

## With Compose

In the folder with `compose.yaml`:

```sh
docker compose pull
docker compose up -d
docker compose logs app | tail
```

`up -d` replaces the container; the data stays in its volume. With the tag `1` you get every 1.x release; a full version (`1.2.3`) stays on one until you change it.

At its start, the new version brings the database up to date when it needs to. Before it changes anything, it copies the SQLite database to `DATA_DIR/backups/pre-migrate-<version>-<time>.db` (the version it had) and refuses to start if that copy fails; the log says `database backed up before the update` with the path. The five newest of these copies are kept. A start without a database change makes none.

On **Postgres** the tool makes no copy: back the database up with `pg_dump` before you update (see [Backup and restore](backup-restore.md#postgres)). The log says so at every start.

Read the release notes before a new major version (`2`): that is where changes you must act on are announced.

## Without Compose

Pull the new image, stop and remove the old container, and start the new one with the same settings and the same volume (`--stop-timeout 30` lets the old one finish writing):

```sh
docker pull ghcr.io/OWNER/monitor:1
docker stop -t 30 monitor && docker rm monitor
docker run -d --name monitor --restart unless-stopped -p 8000:8000 --stop-timeout 30 \
  -e SECRET_KEY=<the same secret> -e BASE_URL=<the same address> \
  -v monitor-data:/data ghcr.io/OWNER/monitor:1
```

## Going back to an older version

An older version does not know a database that a newer one updated: do not just start the old image on it. Go back to the copy made before the update instead, which loses what came in since:

1. Find the copy: Settings → Backup lists the automatic backups ("Before a database update (version 7)"), and the update's log line names it.
2. Stage it while the new version still runs: `docker compose exec app /monitor restore /data/backups/pre-migrate-0007-20261016T120000Z.db` (your file's name). It is checked and waits for the next start.
3. Put the old version's tag in `compose.yaml` (`image: ghcr.io/OWNER/monitor:1.2.3`) and run `docker compose up -d`. The old version starts, moves the newer database to `backups/pre-restore-...` and puts the copy in its place.

On Postgres, restore the `pg_dump` you made before the update, with the tool stopped, then start the old version.

## Changing settings

Change `.env` or `compose.yaml` and run `docker compose up -d` again; Compose restarts the container when its settings changed. Changing `SECRET_KEY` signs everyone out and voids API tokens; changing `BASE_URL` changes the DSNs your apps must use ([Configuration](configuration.md)).

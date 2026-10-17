# Backup and restore

Everything the tool keeps is in one volume, `/data` (`DATA_DIR`): the SQLite database, its automatic backups, and uploaded source maps. There are three ways to back it up.

| Way | What it holds | While the tool runs |
| --- | --- | --- |
| Settings → Backup → Download backup | the database | yes |
| The automatic backups in `DATA_DIR/backups/` | the database, before each update or restore | made by the tool |
| [A copy of the volume](#copy-the-volume) | everything, source maps too | stop it for the copy |

Keep a copy somewhere else than the machine itself, and keep `SECRET_KEY` with it: without the same secret a restored database still has its accounts and passwords, but not its sessions, open links or API tokens.

On Postgres the tool makes no backups and restores none: see [Postgres](#postgres).

## Download a backup

Settings → Backup (admins) → Download backup saves the whole database as one SQLite file, `monitor-backup-<time>.db`, made while the tool keeps running (with SQLite's `VACUUM INTO`, so it is consistent and compact). It needs free disk space of about the database's size for a moment; the page says when there is not enough. Uploaded source maps are not in it: upload them again after a restore (your build does that on its next release).

The file is a normal SQLite database: `sqlite3 monitor-backup-*.db 'PRAGMA integrity_check'` checks it.

## Automatic backups

Before it updates the database (at the first start of a new version) the tool copies it to `backups/pre-migrate-<version>-<time>.db`, and before a restore it moves the database it replaces to `backups/pre-restore-<version>-<time>.db`. The five newest of each kind are kept. Settings → Backup lists them, with the command to go back to the newest. They are on the same disk as the database: they protect against a bad update or restore, not against losing the disk.

## Restore

A restore replaces the whole database with a backup. It never happens while the tool runs: the backup is checked, waits as `DATA_DIR/restore-pending.db`, and the next start puts it in place.

**In Settings → Backup**: choose the file under "Restore a backup" and click "Upload and check". When it passes, the page says "Restart required". Then restart:

```sh
docker compose restart app
```

**On the command line**, for a large file or when the dashboard is not reachable: copy the file into the container and stage it, then restart:

```sh
docker compose cp monitor-backup-20261017T081500Z.db app:/tmp/restore.db
docker compose exec app /monitor restore /tmp/restore.db
docker compose restart app
```

(The copy in `/tmp` goes away when the container is replaced, at the next update.) At the start the tool checks the file again, moves the current database to `backups/pre-restore-<version>-<time>.db`, puts the backup in its place, and updates it when it is from an older version (making a `pre-migrate` copy first, as with any update). The log says `restored the database from the backup that waited`. Then sign in with an account of the backup. Events that arrived between the upload and the restart are not in the restored database; they stay in the pre-restore copy.

Changed your mind before the restart? "Cancel the restore" on the page, or `docker compose exec app /monitor restore --cancel`.

**What is checked**, when the file is staged and again at the start: that it is a SQLite database SQLite finds intact (`PRAGMA integrity_check`, at the start the quicker `quick_check`); that its database is this tool's, at the version of this tool or an older one, with exactly the tables and indexes that version makes and nothing else; that no row refers to a row that is not there; and that its source map rows name files the tool could have made. A file that fails is refused with the reason and nothing changes (in the dashboard: "The file is damaged: ...", "The backup is from a newer version of the tool: ... Update the tool first"). A file that passed when it was staged but fails at the start (it was changed on disk in between) is moved to `restore-rejected-<time>.db`, the log says why, and the tool starts on the database it had.

A backup from an older version whose schema text differs from what this version would make (rare: it can happen when the SQLite inside the tool changed between versions) is refused with "restore it with the version of the tool that made it, then update the tool": run that version once with the backup (see [Updating](updating.md#going-back-to-an-older-version)), then update.

## Copy the volume

The volume holds everything, source maps too. Stop the tool for the copy, so the database files are not written meanwhile. With Compose, the volume is called `<folder>_data` (`docker volume ls` shows it; `monitor_data` for a folder called `monitor`):

```sh
docker compose stop app
docker run --rm -v monitor_data:/data:ro -v "$PWD":/backup alpine \
  tar czf /backup/monitor-data-$(date +%Y%m%d).tar.gz -C /data .
docker compose start app
```

To put such a copy back (this replaces everything in the volume, without the checks above):

```sh
docker compose stop app
docker run --rm -v monitor_data:/data -v "$PWD":/backup alpine \
  sh -c 'rm -rf /data/* /data/.[!.]* && tar xzpf /backup/monitor-data-20261017.tar.gz -C /data'
docker compose start app
```

The same copy moves the tool to another machine. There, in a folder with the same name (so the volume gets the same name), put `compose.yaml` and `.env` (the same `SECRET_KEY`) and the copy, fill the volume, and start the tool. No container exists there yet, so there is nothing to stop:

```sh
docker run --rm -v monitor_data:/data -v "$PWD":/backup alpine tar xzpf /backup/monitor-data-20261017.tar.gz -C /data
docker compose up -d
```

Compose warns that the volume `monitor_data` was not created by it, and uses it.

## Postgres

With `DATABASE_URL` the database is Postgres: back it up with your database host's backups or with `pg_dump`, also before every update (the tool does not do it for you, and says so in its log and on Settings → Status and Backup):

```sh
pg_dump --format=custom --file=monitor.dump "$DATABASE_URL"
```

To put it back, stop the tool first, then:

```sh
pg_restore --clean --dbname="$DATABASE_URL" monitor.dump
```

`DATA_DIR` still holds the uploaded source maps; copy the volume for those.

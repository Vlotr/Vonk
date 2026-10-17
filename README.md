# Monitoring tool (`monitor`)

A light, self-hosted tool for errors and page statistics. Your apps keep their existing SDKs: the tool pretends to be Sentry (errors from Laravel and the browser) and PostHog (page views and events). It runs as one Go binary with the dashboard built in, in one small container (about 21 MB, for x86-64 and ARM) with all data on one volume. SQLite by default; Postgres if you want it. The name is not decided yet; the dashboard shows `[Tool name]`.

What it does:

- **Errors**: Sentry SDKs send to the tool's DSN. Errors are grouped into issues with their stack trace (source maps for minified browser code), can be resolved, ignored and reopened, and come back when they return. All projects shows which projects have new errors.
- **Page views**: posthog-js (without cookies) sends page views; each project's Pages screen shows views, visitors, the average time on a page and the errors on it. Custom events (`posthog.capture(...)`) are counted on the Events screen.
- **The journey before an error**: with the same user id in both SDKs, an issue lists the pages the user saw in the half hour before it.
- **Alerts** by mail and Slack: new issues, returning issues, spikes, and daily, weekly or monthly summaries. Admins hear about the tool itself, such as a disk running full.
- **Accounts**: the first visitor creates the admin; others join by invite, as admins or as members who see only their projects.
- **Your data stays yours**: no IP addresses stored, passwords and tokens filtered out before storage, retention per project, backup and restore in Settings, and deleting one person's data by user id.

## Get started

[Install it with Docker Compose](docs/install.md): start the container, create the first account, add a project and send a first error, in about ten minutes.

| Guide | What it covers |
| --- | --- |
| [Install](docs/install.md) | Docker Compose (or `docker run`), the first account, a project, a first error |
| [HTTPS with Caddy](docs/https-caddy.md) | a certificate in front of the tool ([`examples/caddy`](examples/caddy)); against adblockers |
| [Configuration](docs/configuration.md) | every setting: `SECRET_KEY`, `BASE_URL`, `DATABASE_URL` (Postgres), `MAIL_URL`, ... |
| [Accounts, projects and SDK setup](docs/sdk-setup.md) | Laravel, the browser, posthog-js, the same user in both, source maps, alerts, **tested SDK versions** |
| [Updating](docs/updating.md) | new versions, the automatic backup before a database update, going back |
| [Backup and restore](docs/backup-restore.md) | downloads, automatic backups, restoring, copying the volume, Postgres |
| [Privacy and retention](docs/privacy.md) | what is stored and what is not, retention, deleting a person's data, cookies |
| [Troubleshooting](docs/troubleshooting.md) | when something does not arrive or start |

Tested with `sentry/sentry-laravel` 4.29, `sentry/sentry` 4.34, `@sentry/browser` 11.8, `posthog-js` 1.438.12, `@sentry/cli` 3.8 and `@sentry/vite-plugin` 11.8 ([details](docs/sdk-setup.md#tested-sdk-versions)).

## Status

Version 1 is complete: errors, issues, accounts, alerts, health, page views, the journey, releases, custom events, source maps, optional Postgres, backup and restore, and deleting a user's data. Images are built for `linux/amd64` and `linux/arm64` by CI and published to the GitHub Container Registry for each release tag.

License: not decided yet. Whether the tool becomes open source, and under which license, is an open question of the project (see the spec's open questions); until it is answered, the repository has no license file.

## Development

You need Go 1.27, Node 24 and npm; Docker for the image and the Postgres tests; PHP 8.3+ and Composer for the end-to-end tests.

```sh
make dev            # Go on :8000 and the dashboard with hot reload on http://localhost:5173 (open that one); data in ./data
make test           # go vet, Go tests with -race, web typecheck and Vitest, the fixture leak checks
make test-postgres  # the Go tests that use a database, against Postgres 17 in a throwaway container
make e2e            # Playwright: the real binary, the Laravel, browser and PostHog examples, the dashboard in Chromium
make build          # bin/monitor with the dashboard embedded
make docker         # the image monitor:dev for this machine's platform
```

A multi-platform image, as CI builds it: `make docker-multiarch` (`linux/amd64` and `linux/arm64`, kept in the build cache; the build stages cross-compile, so no emulation is needed). Docker Desktop's builder does this as it is; with Docker Engine's default builder, make a builder that can first, once: `docker buildx create --use`.

CI (`.github/workflows/ci.yml`) runs the same targets on every push and pull request: `make test` and `make build`, `make test-postgres`, `make e2e`, and the multi-platform image build.

Releasing: push a tag `v1.2.3`. CI then pushes the image to `ghcr.io/<owner>/monitor` with the tags `1.2.3`, `1.2`, `1` and `latest` (a pre-release such as `v1.3.0-rc.1` gets only its own tag), labelled with the repository (`org.opencontainers.image.source`), so the package links to it. GitHub makes a new package private: after the first release, make it public once in the package's settings, or nobody else can pull it. Then replace `OWNER` in `docs/` and `examples/caddy` with the owner.

`make e2e` saves screenshots of the dashboard as it looked during the run in `e2e/screenshots/`.

The examples send real traffic to a running tool: [`examples/laravel`](examples/laravel) (sentry-laravel), [`examples/browser`](examples/browser) (`@sentry/browser`, and source maps with the vite plugin) and [`examples/posthog`](examples/posthog) (posthog-js and `@sentry/browser` with the same user id). Each has a README.

`CLAUDE.md` describes the layout, the conventions and how each part works; `docs/decisions.md` lists the choices made along the way, one line each. The spec is `concept-monitoring-tool.md` (in Dutch).

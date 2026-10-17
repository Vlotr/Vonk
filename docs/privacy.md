# Privacy and retention

The tool runs on your own server, so the data your apps send stays with you. This page says what it keeps, what it leaves out, how long it keeps it, and how to delete one person's data on request. It is not legal advice: whether you need consent, a cookie banner or an entry in your privacy statement depends on your situation and your country's law, so check it with a lawyer.

## What is stored

- **Errors** (Sentry SDKs): the error and its stack trace, the page or request it happened on, the browser and system (from the User-Agent), the release and environment, breadcrumbs, and the user fields the SDK sends (the id, and more when you let it; see below).
- **Page views and page leaves** (posthog-js): the page's path (without query string or fragment), the time, the time on the page, the visitor's id (`distinct_id`), session and tab ids, and whether it was a bot.
- **Custom events** (`posthog.capture`): the name, the page, the user id, and the properties your app sent. PostHog's own `$` properties are left out.
- **Accounts** of the people who use the dashboard: email address, role, a password hash (argon2id), sessions (as keyed hashes).

The dashboard's statistics read hourly summaries, in which visitors are kept as a mergeable count (a HyperLogLog sketch of hashed ids), not as a list of ids.

## What is left out

- **IP addresses are never stored**, of visitors or of anyone else: Sentry's `user.ip_address`, the IP headers (`X-Forwarded-For`, `X-Real-IP`, `CF-Connecting-IP`, ...), `REMOTE_ADDR` and the like in the request's environment, and PostHog's `$ip` are dropped before anything is stored. The SDKs' `{{auto}}` (which asks the server to fill in the client's address) is dropped too. Request logs leave out client addresses. There is no geo lookup; the country shows only when an SDK sends one itself.
- **Secrets are filtered**: before an event is stored, every value whose key looks sensitive becomes `[Filtered]`, at any depth (request headers, cookies, query strings, form and JSON bodies, breadcrumbs, `extra`, `contexts`, frame variables, tags, PostHog properties). Keys match when they contain `password`, `passwd`, `pwd`, `secret`, `token`, `api_key`, `apikey`, `auth`, `authorization`, `cookie`, `session`, `csrf`, `xsrf`, `credit`, `card`, `cvv`, `iban`, `ssn` or `set-cookie`, ignoring case and `-`, `_`, `.` and spaces (`X-Api-Key` matches). Request cookies are dropped whole. Passwords inside URLs (`postgres://app:secret@db`) are filtered too.
- **Secrets in paths**: path segments of page addresses that look like a UUID, a hash or a token are masked (`/reset-password/:hash`) in page views and in an error's URL, Referer header, breadcrumbs and stack frames, so a password reset link is never stored with its token. Numbers and words stay.

What is **kept on purpose**: a query parameter whose key is not on the list above keeps its value, in an error's URL, its Referer header or a breadcrumb. `https://shop.example.com/search?email=jan@example.com` is stored as it is. Keep personal data out of URLs (send it in a POST body), and leave the SDKs' own personal data off: `send_default_pii` in sentry-laravel (`SENTRY_SEND_DEFAULT_PII`, off by default) and `sendDefaultPii` in `@sentry/browser` (off by default). With it on, sentry-laravel sends the signed-in user's email address and more, which the tool stores. Exception messages are stored as your code writes them: do not put personal data in them.

## How long

Each project keeps its data for 30, 90 (the default), 180 days, 1 year or indefinitely (Settings → Projects → Edit). Once a day, the tool deletes the project's events, issues last seen before that time, page views and leaves, custom events, their hourly summaries, and source maps uploaded before it. Backups keep what they held when they were made; the automatic ones are replaced after five newer ones.

## Deleting a person's data on request

For a request to erase someone's data (GDPR article 17), delete everything stored with their **user id**: the id your apps give the SDKs (Sentry's `user.id`, PostHog's `distinct_id`, see [the same user in errors and page views](sdk-setup.md#the-same-user-in-errors-and-page-views)).

- **Settings → Privacy** (admins): the user id, every project or only some, and a second question before anything goes.
- **On the server**: `docker compose exec app /monitor delete-user-data --user-id 42` (add `--project 1`, repeated, to limit it to those projects).

It deletes, for that exact id: error events, page views and their page leaves, and custom events. Issues that only this user had are removed; other issues are recounted, and when the newest event of an issue was this user's, the issue takes the text of its newest remaining event. The page and event statistics are rebuilt within a minute (or at the next start, when the tool was stopped). The answer says how many of each were deleted.

What it does **not** reach:

- **Data without that id.** posthog-js sends events under an anonymous id until your app calls `identify`. Those page views and events (the visit before signing in) are stored under the anonymous id, even when they share a session with the identified ones, and are not deleted with the user id. Errors without a user id are not either. Delete by the anonymous id too when you know it.
- **Backups** made before the deletion keep the data until they are replaced: the downloads you keep elsewhere, and the automatic backups (after five newer ones of their kind). Free pages inside the SQLite file can hold deleted bytes until SQLite reuses them; a downloaded backup never contains them.
- **Other copies**: logs of your reverse proxy, and notifications already sent by mail or Slack (they name the error, not the user).

The tool logs one line per deletion with the admin who made it (or `by=command-line`) and the number of rows, never the user id. Every log line that names a request's path (the request log, and the error lines of the dashboard, sign-in and upload APIs) shows the user activity page with the user id masked (`/projects/1/users/:userId`), so looking a person up leaves no id in the log either. Log lines never name an SDK's user id otherwise.

## Cookies

- The dashboard sets one cookie for people who sign in: the session (`monitor_session`, or `__Host-monitor_session` on HTTPS), needed to stay signed in.
- The setup this documentation and the dashboard give for posthog-js uses `persistence: 'memory'`: posthog-js then stores nothing in the visitor's browser, no cookies and no local storage. The Sentry browser SDK sets no cookies.
- Without `persistence: 'memory'` posthog-js keeps an id in a cookie (and local storage) so returning visitors are recognised. That may need consent where you are.

Whether you need a cookie banner or consent for error tracking and page statistics, also without cookies, is a legal question for your situation: ask a lawyer.

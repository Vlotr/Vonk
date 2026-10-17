# HTTPS with Caddy

The tool speaks plain HTTP on port 8000. For HTTPS, put a reverse proxy in front of it. This page uses [Caddy](https://caddyserver.com), which gets a certificate from Let's Encrypt by itself and renews it. Any other proxy (Traefik, nginx) works the same way: forward everything to port 8000, pass the visitor's address in `X-Forwarded-For`, and set `BASE_URL` to the `https://` address. nginx needs two settings beyond that, because its defaults are too small: `client_max_body_size` (1 MB by default; SDK envelopes can be 20 MiB, source map uploads 33 MiB, and a restore upload is as large as the database, so `client_max_body_size 0;` or a size of your own) and `proxy_read_timeout` (60 s by default; a backup download of a large database waits longer for its copy). With `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;` and `proxy_set_header Host $host;` the rest works as with Caddy.

The files are in [`examples/caddy`](../examples/caddy): `compose.yaml` (Caddy and the tool) and `Caddyfile`. They were tested with Caddy 2.11 (the `caddy:2.11` image).

## Steps

You need a domain name whose DNS points at the machine (an `A` or `AAAA` record for `monitor.example.com`), and ports 80 and 443 open to the internet: Let's Encrypt checks the domain over them.

1. Copy `examples/caddy/compose.yaml` and `examples/caddy/Caddyfile` into a folder, and replace `OWNER` in the image name (see [Install](install.md)).
2. Make `.env` next to them, with your domain and a random secret:

   ```sh
   printf 'DOMAIN=monitor.example.com\nSECRET_KEY=%s\n' "$(openssl rand -hex 32)" > .env
   chmod 600 .env
   ```

3. Start both:

   ```sh
   docker compose up -d
   curl https://monitor.example.com/health
   ```

   The first start takes a few seconds longer while Caddy gets the certificate; `docker compose logs caddy` shows it.
4. Open `https://monitor.example.com` and create the first account right away (see [Install](install.md#3-create-the-first-account)).

`BASE_URL` is `https://` plus `DOMAIN` (the compose file sets it), so the DSNs the tool shows use HTTPS. The tool has no published port in this setup: only Caddy reaches it, over the network Compose makes for the two.

Already running the tool from the [install](install.md) folder with plain HTTP? Put these two files in that folder instead of its `compose.yaml`, add `DOMAIN=monitor.example.com` to its `.env` (the `BASE_URL` line there is no longer used), and run `docker compose up -d`. Compose names the volume after the folder (`<folder>_data`), so the data stays. Then update the DSNs in your apps, which carry the address, and make new API tokens for source maps, which carry it too.

## What changes with HTTPS

- The session cookie becomes `__Host-monitor_session`, sent over HTTPS only. Everyone signs in again once.
- Sign-in limits count per visitor: Caddy adds the visitor's address to `X-Forwarded-For`, and the tool trusts that header only from a proxy on a private network address or the same machine, as Caddy is here. The address is used in memory for the limit and never stored.
- The SDKs send to the `https://` DSN. Browsers refuse to send from an HTTPS page to a plain HTTP address, so a site on HTTPS needs the tool on HTTPS too.

## Try it on your own machine

With `DOMAIN=localhost`, Caddy makes a certificate with a local CA of its own instead of asking Let's Encrypt. Browsers and curl do not know that CA, so they warn (`curl -k` skips the check):

```sh
printf 'DOMAIN=localhost\nSECRET_KEY=%s\n' "$(openssl rand -hex 32)" > .env
docker compose up -d
curl -k https://localhost/health
```

Ports 80 and 443 must be free on your machine for this.

## Against adblockers: your own domain or path

Adblockers block requests to well-known analytics and error-tracking hosts and often to paths that look like them. A domain of your own, such as `monitor.yourshop.com`, is not on their lists, which is why the tool runs on your domain anyway.

Going one step further, the browser SDKs can send to a path of your own site, so their requests look like any other request to it. With Caddy in front of your site, forward only the paths the browser SDKs use, under a prefix of your choice, and strip the prefix:

```
shop.example.com {
	@monitor path /m/api/*/envelope/ /m/e/* /m/i/v0/e/* /m/batch/* /m/flags/* /m/decide/* /m/array/* /m/static/* /m/s/*
	handle @monitor {
		uri strip_prefix /m
		reverse_proxy https://monitor.example.com {
			header_up Host {upstream_hostport}
		}
	}
	# ... the rest of your site
}
```

Forward only these paths, not all of `/m/*`: that would put the dashboard and its sign-in page on your shop's address too, where they do not belong (its cookies and its sign-in limits would then live on the shop's domain). When the tool runs next to that Caddy (as in `examples/caddy`), `reverse_proxy app:8000` takes the place of the `https://` upstream. Then use `api_host: 'https://shop.example.com/m'` for posthog-js, and for the browser's Sentry SDK the DSN with the path added before the project id: `https://<key>@shop.example.com/m/1` instead of `https://<key>@monitor.example.com/1`. The dashboard stays on its own domain. (Tested next to the tool: posthog-js 1.438 and `@sentry/browser` 11.8 sent their configuration, page view, event and error requests through `/m/`.)

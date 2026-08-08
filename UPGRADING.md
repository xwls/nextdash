# Upgrading nextDash

## Mandatory administrator authentication

This release is a **breaking, fail-closed security upgrade**. An older deployment without `NEXTDASH_ADMIN_PASSWORD_HASH` will exit with a clear configuration error; it will not start in an open mode.

1. Generate a password hash with hidden input:

   ```sh
   go run . hash-password
   # or
   docker run --rm -it ghcr.io/jordibrouwer/nextdash:latest hash-password
   ```

2. Put the result in an ignored `.env` file. Use single quotes so Compose does not interpret the `$` characters in the PHC string:

   ```dotenv
   NEXTDASH_ADMIN_USERNAME=admin
   NEXTDASH_ADMIN_PASSWORD_HASH='$argon2id$v=19$m=65536,t=3,p=2$...$...'
   ```

   Source launches (`go run .` and the compiled binary) now load this file automatically from the current working directory. Existing process environment variables still take precedence.

3. For production HTTPS, keep `NEXTDASH_AUTH_COOKIE_SECURE=1` (the default). Only local plain-HTTP development should use `NEXTDASH_AUTH_COOKIE_SECURE=0`.

4. If the browser extension is used, configure a long random `NEXTDASH_WRITE_TOKEN` on the server and paste the same value into the extension settings. The token only grants the extension's limited bookmark APIs.

5. Recreate/restart the container. All Sessions are in memory, so every process restart requires a fresh login.

## `/data/` public access change

The complete data directory is no longer an HTTP file server. Public requests are limited to validated icons, favicon files, and fonts. Business JSON, settings, backups, ZIP archives, logs, temporary files, nested legacy paths, and directory listings are intentionally unavailable. No data-file migration is required.

## HTTPS

Production deployments must terminate HTTPS at Caddy, Cloudflare Tunnel/Access, nginx, Traefik, or another reverse proxy. The app does not terminate TLS itself.

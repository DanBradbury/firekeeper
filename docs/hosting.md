# Hosting at firekeeper.danbradbury.net

This is the runbook for running the dashboard on a small VPS behind HTTPS.
Steps marked **Owner** need your accounts or your hands; nothing in the repository
does them for you. For a quick local setup without TLS, see
[self-hosting.md](self-hosting.md).

Uploads contain full transcripts and redaction is best effort. Keep signup
closed, and only upload from machines you control.

## What runs

`deploy/compose.yml` runs two containers:

- **firekeeper**: the server, listening on the Compose network only (nothing is
  published), database on the named volume `firekeeper-data`. Health check:
  `GET /healthz`, which needs no credentials and returns no data.
- **caddy**: terminates TLS on ports 80 and 443 with automatic certificates,
  redirects HTTP to HTTPS, gzips pages, and does not buffer `/v1/stream` so
  server-sent events arrive as they happen.

The server refuses to start without any account or token, so it is never open
to the network: create the first account before the first `up`.

## One-time setup (Owner)

1. **Provision a host.** Any Linux VPS with 1 vCPU and 1 GB RAM is enough.
   Use a provider you trust with the data.
2. **DNS.** Create an `A` record for `firekeeper.danbradbury.net` pointing at the
   host's public IPv4 address (and `AAAA` for IPv6, if you use it). Caddy cannot get
   a certificate until this resolves to the host.
3. **Firewall.** Allow inbound 22 (SSH, ideally from your address only), 80 and
   443 (TCP, and 443 UDP for HTTP/3). Block everything else; port 7777 must not
   be reachable.
4. **Install Docker** with the Compose plugin, following Docker's instructions
   for your distribution.
5. **Get the code or image.**
   ```sh
   git clone https://github.com/DanBradbury/firekeeper /opt/firekeeper
   cd /opt/firekeeper/deploy
   ```
   To run a published image instead of building, set
   `FIREKEEPER_IMAGE=ghcr.io/danbradbury/firekeeper:vX.Y.Z` in `deploy/.env`
   (if the package is private, `docker login ghcr.io` first). Otherwise
   `docker compose build` builds on the host.
6. **Create the first account.** This prompts for a password on the terminal:
   ```sh
   docker compose run --rm firekeeper serve admin create-account \
     --db /data/firekeeper.db --email you@example.com
   ```
7. **Start it** (signup stays `closed`, the default in `compose.yml`):
   ```sh
   docker compose up -d
   docker compose ps          # both services running, firekeeper healthy
   curl -fsS https://firekeeper.danbradbury.net/healthz
   ```
8. **Sign in** at https://firekeeper.danbradbury.net/ and link each machine
   with `firekeeper login --server https://firekeeper.danbradbury.net`.
9. **Schedule backups** (below) and test a restore once.

## Backups

`deploy/backup.sh` writes a consistent copy with SQLite `VACUUM INTO` while the
server runs, checks it, and keeps the newest 14 (`KEEP` changes that).
Copies are mode 600 in `BACKUP_DIR` (default `/var/backups/firekeeper`) on the
host. Nothing leaves the host unless you add that yourself, for example an
`rsync` to storage you own, which is up to you. Backups contain every
transcript and token hash; never commit them.

```sh
BACKUP_DIR=/var/backups/firekeeper ./backup.sh
```

Schedule it with the systemd units `firekeeper-backup.service` and
`firekeeper-backup.timer` (copy to `/etc/systemd/system/`, then
`systemctl enable --now firekeeper-backup.timer`), or with cron:

```cron
15 3 * * * cd /opt/firekeeper/deploy && ./backup.sh >> /var/log/firekeeper-backup.log 2>&1
```

### Restore test

Do this after setting up backups and after every upgrade of the schema:

```sh
./restore-test.sh /var/backups/firekeeper/firekeeper-<stamp>.db
```

It loads the backup into a throwaway volume, starts the server on it, waits
for it to become healthy, and compares session and event counts with the
backup file. It prints `restore-test: ok` and removes what it created. It
shows counts only, never transcript text. To really restore:

```sh
docker compose down
docker run --rm --user 0 --entrypoint sh \
  -v firekeeper_firekeeper-data:/data -v /var/backups/firekeeper:/b:ro \
  "${FIREKEEPER_IMAGE:-firekeeper:local}" -c 'cp /b/firekeeper-<stamp>.db /data/firekeeper.db && chown -R 10001:10001 /data'
docker compose up -d
```

The volume is `<project>_firekeeper-data`; the project defaults to the
directory name (`deploy_firekeeper-data` if you run from `deploy/`). Check
with `docker volume ls`, and remove stale `firekeeper.db-wal` and `-shm`
files in the volume before starting.

## Upgrade

```sh
cd /opt/firekeeper/deploy
./backup.sh
git pull                       # or: set FIREKEEPER_IMAGE to the new tag
docker compose build           # skip when using a published image
docker compose up -d
curl -fsS https://firekeeper.danbradbury.net/healthz
```

The database migrates on start. Take the backup first: a migration is not
reversed by running an older image.

## Rollback

1. Check out the previous version (or set the previous `FIREKEEPER_IMAGE`)
   and `docker compose up -d`. This is enough when the new version did not
   change the database schema.
2. If it did, or data was damaged, `docker compose down`, restore the
   pre-upgrade backup as shown above, then start the previous version.
   Events uploaded since that backup are lost from the server; running
   `firekeeper report` on each machine re-sends anything newer, and the
   server drops what it already has.

## Local trial of this stack

On any machine with Docker, no DNS needed:

```sh
cd deploy
export FIREKEEPER_DOMAIN=localhost HTTP_PORT=8080 HTTPS_PORT=8443
docker compose run --rm firekeeper serve admin create-account --db /data/firekeeper.db --email me@example.test
docker compose up -d
curl -k https://localhost:8443/healthz
```

Caddy issues a certificate from its own internal CA, so browsers and `curl`
warn unless you use `-k`. Machines should not upload to this: their
`firekeeper` would reject the certificate.

## Known limits

- Per-IP rate limiting of failed logins uses the connection's address, not
  `X-Forwarded-For`, so behind Caddy every client shares one budget.
- Authentication turns on when the server starts. If you create the first
  token or account while it runs, restart it. The steps above create the
  account first, so this does not arise.

## Hosting on a server that already runs nginx

If the host already serves other sites with nginx and `certbot --nginx` (as the
`golf` and `money` subdomains do), do not use `deploy/compose.yml`: its Caddy
would fight nginx for ports 80 and 443. Use `deploy/compose.nginx.yml`
instead. It runs only the app, published on `127.0.0.1:7777`, and nginx proxies
to it. Deploys come from the **Publish and deploy** workflow over SSH.

The nginx vhost (`deploy/nginx/firekeeper.danbradbury.net.conf`) sets
`X-Forwarded-Proto`, turns off buffering and compression for `/v1/stream`, and
allows 6 MB bodies (ingest requests are at most 5 MiB).

### One-time setup (Owner)

1. **DNS.** Add an `A` record for `firekeeper.danbradbury.net` pointing at the host.
2. **Docker.** Check it is installed and that the `deploy` user can run it
   without sudo: `ssh deploy@HOST docker ps`. If not, install it and run
   `sudo usermod -aG docker deploy`, then log in again.
3. **Port.** Check that 7777 is free on the host loopback:
   `ss -ltn | grep 7777`. If not, put `FIREKEEPER_PORT=NNNN` in
   `/opt/firekeeper/.env` and change `127.0.0.1:7777` in the vhost to match.
4. **nginx vhost.** From a checkout on the host (or copy `deploy/`):
   ```sh
   sudo bash deploy/setup-nginx.sh
   ```
   This creates `/opt/firekeeper`, installs the vhost, and reloads nginx.
5. **Certificate**, once DNS resolves:
   ```sh
   sudo certbot --nginx -d firekeeper.danbradbury.net
   ```
6. **Image access.** Either make the GHCR package `firekeeper` public
   (GitHub, Packages, package settings), or on the host run
   `docker login ghcr.io` once with a token that has only `read:packages`.
7. **GitHub secrets.** In the firekeeper repository add an environment named
   `prod` with secrets `SERVER_IP` and `SSH_PRIVATE_KEY` (a key authorised for
   `deploy`). Secrets are per repository: golfeo's do not carry over. Optionally
   add `SSH_KNOWN_HOSTS` (output of `ssh-keyscan HOST`) so the workflow does not
   trust the host on first sight. Consider required reviewers on `prod`.
8. **Publish an image.** Push a tag: `git tag v0.1.0 && git push origin v0.1.0`.
   Wait for the **image** job to finish.
9. **Create the first account** on the host, before the first deploy. The
   compose file needs an image, so give it the tag:
   ```sh
   cp deploy/compose.nginx.yml /opt/firekeeper/   # from your checkout on the host
   cd /opt/firekeeper
   echo FIREKEEPER_IMAGE=ghcr.io/danbradbury/firekeeper:v0.1.0 > .env
   docker compose -f compose.nginx.yml run --rm firekeeper \
     serve admin create-account --db /data/firekeeper.db --email you@example.com
   ```
10. **Deploy.** GitHub, Actions, **Publish and deploy**, Run workflow, tag
    `v0.1.0`. It copies the compose file and backup scripts, pulls the image,
    restarts, and waits for `/healthz`.
11. **Backups.** Scripts are in `/opt/firekeeper`. Use the systemd units or
    cron line above with `/opt/firekeeper/backup.sh`; it finds
    `compose.nginx.yml` itself. Run `/opt/firekeeper/restore-test.sh` on a
    backup once; it uses the image in `.env`.

### Upgrade and rollback

Run `./backup.sh` on the host, push a new tag, wait for the image, then run the
workflow with that tag. To roll back, run the workflow with the earlier tag.
If the new version changed the database schema, restore the pre-upgrade
backup too (see Rollback above).

### Checks on this path

The vhost, the loopback compose file and SSE through nginx were tested locally
against `nginx:alpine`; the workflow passes `actionlint`. A real SSH deploy, the
GHCR pull and the certificate were not tested here.

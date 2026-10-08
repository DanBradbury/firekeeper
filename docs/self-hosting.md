# Self-hosting the dashboard

The dashboard server is `firekeeper serve`. This page runs it in a container,
creates tokens, puts TLS in front of it, and backs up its database.

Uploads contain full transcripts. Redaction is best effort and does not catch
every secret (see the README). Host the server only where you trust the
operator and the network.

## Run it

```sh
docker compose up -d
```

This builds the image and serves the UI at http://127.0.0.1:7777/. To use
another host port, set `FIREKEEPER_PORT`. The database lives in the named
volume `firekeeper-data`, mounted at `/data/firekeeper.db`. The container
runs as a non-root user.

`just docker-build` builds the image as `firekeeper:local` without Compose.

The compose file publishes the port on the host's loopback only and starts
the server with `--insecure`. Until a token or account exists, the API is
open to anything that can reach that port. Create credentials first thing.

## Tokens

Create them inside the container. The token prints once; only its hash is
stored.

```sh
# Read token, for the web UI login page and API reads.
docker compose exec firekeeper firekeeper serve token create --db /data/firekeeper.db --name me --scope read

# Ingest token for one machine. Use the id in ~/.firekeeper/machine-id on that machine.
docker compose exec firekeeper firekeeper serve token create --db /data/firekeeper.db --name laptop --scope ingest --machine MACHINE_ID

docker compose exec firekeeper firekeeper serve token list --db /data/firekeeper.db
docker compose exec firekeeper firekeeper serve token revoke --db /data/firekeeper.db NAME_OR_ID
```

**Restart after creating the first token or account** (`docker compose
restart`). The server decides at startup whether authentication is on, so
until then it keeps answering without credentials.

For accounts, sign-up modes (`--signup`), and the admin commands, see the
[README](../README.md#accounts). Admin commands work directly on the
database, for example:
`docker compose exec firekeeper firekeeper serve admin create-account --db /data/firekeeper.db`.

## Upload from a machine

```sh
export FIREKEEPER_TOKEN=...      # the ingest token
firekeeper report --server http://127.0.0.1:7777 --provider codex --dry-run
firekeeper report --server http://127.0.0.1:7777 --provider codex
```

Drop `--dry-run` only when you are happy with what it reports. `daemon` and
`backfill` take the same `--server`, `--provider`, and `--token`.

## TLS behind a reverse proxy

The server speaks plain HTTP. Terminate TLS in a reverse proxy on the same
host and keep the container's port on loopback. Two things matter:

- Send `X-Forwarded-Proto: https`. The server marks the session cookie
  `Secure` only when it sees HTTPS or that header.
- Do not buffer `/v1/stream`. It is a server-sent events stream and must
  flush as events arrive.

Caddy, which obtains certificates itself:

```caddyfile
dash.example.com {
	encode gzip
	reverse_proxy 127.0.0.1:7777 {
		flush_interval -1
	}
}
```

nginx (certificates managed separately):

```nginx
location / {
    proxy_pass http://127.0.0.1:7777;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;
    proxy_read_timeout 1h;
}
```

Point reporters at the HTTPS URL. Do not expose port 7777 itself.

The server takes the client address for per-IP rate limiting from the
connection, not from `X-Forwarded-For`. Behind a proxy all clients share the
proxy's address and one failed-login budget.

## Backups

The whole state is one SQLite file in WAL mode. Do not copy the file while the
server runs; take a consistent snapshot with `VACUUM INTO`. The image includes
`sqlite3` for this.

```sh
docker compose exec firekeeper sh -c \
  'sqlite3 /data/firekeeper.db "VACUUM INTO '"'"'/data/backup.db'"'"'"'
docker compose cp firekeeper:/data/backup.db ./firekeeper-$(date +%F).db
docker compose exec firekeeper rm /data/backup.db
```

`VACUUM INTO` refuses to overwrite, hence the `rm`. A backup holds every
transcript and the token hashes: store it with the same care as the
database, and never commit it.

To restore, stop the server, put the file in place as `/data/firekeeper.db`
(and remove any `firekeeper.db-wal` and `-shm` beside it), and start again:

```sh
docker compose down
docker run --rm -v firekeeper_firekeeper-data:/data -v "$PWD":/b alpine \
  sh -c 'rm -f /data/firekeeper.db-wal /data/firekeeper.db-shm; cp /b/firekeeper-DATE.db /data/firekeeper.db; chown -R 10001:10001 /data'
docker compose up -d
```

The volume name is `<project>_firekeeper-data`; `docker volume ls` shows it.

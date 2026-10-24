# Deployment guide (WAGO fork)

This guide deploys this fork of libreDesk with a single WAGO device. The WAGO
service is separate; see [wago-integration.md](wago-integration.md) for the
integration details and the webhook path.

For Docker, this repository always builds its own image (`libremitu:local`).
It never pulls `libredesk/libredesk` from Docker Hub.

## 1. Architecture

```text
WhatsApp customer
      │
      ▼
WAGO (single device, REST + webhook)
      │  POST /webhooks/whatsapp/<inbox_id>
      ▼
libreDesk (this repo) ─── PostgreSQL
      │                └─ Redis
      ├─ agents / admin UI
      └─ outbound /send/*, /message/*/read → WAGO
```

Required services:

- libreDesk (this fork)
- PostgreSQL 17
- Redis 7
- WAGO with one logged-in device

## 2. Requirements

### Docker deployment

- Docker Engine 24+
- Docker Compose v2
- A public HTTPS hostname for libreDesk (needed by the WAGO webhook)
- WAGO reachable from libreDesk

### Binary deployment

- Go 1.25+ (`go.mod` requires Go 1.25)
- Node.js 22 and pnpm 9 (frontend build)
- `make`, `git`, `curl`
- PostgreSQL 17 and Redis 7
- A Linux service manager (systemd examples below)

## 3. Prepare the configuration

```bash
git clone https://github.com/<your-account>/libremitu.git
cd libremitu
cp config.sample.toml config.toml
```

Generate a stable encryption key:

```bash
openssl rand -hex 16
```

Put it in `config.toml`:

```toml
[app]
encryption_key = "<32-character-hex-key>"
```

> Keep this key forever. Changing it makes stored secrets unreadable
> (inbox credentials, OAuth tokens, etc.).

Set at least these values:

```toml
[app.server]
address = "0.0.0.0:9000"   # Docker default; for a host install use 127.0.0.1:<port> behind the proxy
max_body_size = 104857600   # 100 MB, must match your reverse proxy

[db]
host = "db"                 # 127.0.0.1 for a host install
port = 5432
user = "libredesk"
password = "<strong-db-password>"
database = "libredesk"
ssl_mode = "disable"        # use "require" for managed TLS databases

[redis]
address = "redis:6379"      # 127.0.0.1:6379 for a host install

[upload.fs]
upload_path = "uploads"

[wago]
base_url = "https://wago.example.com"
device_id = "<WAGO device id>"
username = "<WAGO basic auth user>"
password = "<WAGO basic auth password>"
webhook_secret = "<same value as WAGO WHATSAPP_WEBHOOK_SECRET>"
```

`wago.base_url` may include a subpath, for example `https://host/gowa`.
Leave `username`/`password` empty if WAGO has no Basic Auth.

### Environment variables

Any config key can be supplied as `LIBREDESK_*`; double underscores become dots:

```bash
LIBREDESK_APP__ENCRYPTION_KEY=<32-char-key>
LIBREDESK_DB__HOST=db
LIBREDESK_DB__PASSWORD=<db-password>
LIBREDESK_REDIS__ADDRESS=redis:6379
LIBREDESK_WAGO__BASE_URL=https://wago.example.com
LIBREDESK_WAGO__DEVICE_ID=<device-id>
LIBREDESK_WAGO__USERNAME=<basic-auth-user>
LIBREDESK_WAGO__PASSWORD=<basic-auth-password>
LIBREDESK_WAGO__WEBHOOK_SECRET=<webhook-secret>
LIBREDESK_UPLOAD__FS__UPLOAD_PATH=/libredesk/uploads
```

Environment variables override `config.toml`.

## 4. Deploy with Docker Compose

```bash
mkdir -p uploads
docker compose build
docker compose up -d
```

What happens:

- `docker-compose.yml` builds the local image `libremitu:local`.
- The app container runs:

```bash
./libredesk --install --idempotent-install --yes --config /libredesk/config.toml
./libredesk --upgrade --yes --config /libredesk/config.toml
./libredesk --config /libredesk/config.toml
```

Set the System user password:

```bash
docker exec -it libredesk_app ./libredesk --set-system-user-password --config /libredesk/config.toml
```

Open `http://localhost:9000` and log in as `System`.

### Docker operations

```bash
docker compose logs -f app          # application logs
docker compose ps                   # service status
docker compose build --no-cache     # rebuild after code changes
docker compose up -d                # apply the new image
docker compose restart app
docker compose down                 # stop (volumes are kept)
```

Do **not** use `docker compose pull` for the `app` service: the image is built
locally. The database and Redis images are standard upstream images.

### Custom host port

The host port is configurable without touching `config.toml`. Copy `.env.example`
to `.env` (Compose loads it automatically) and edit:

```bash
cp .env.example .env
# .env
LIBREDESK_BIND_ADDR=127.0.0.1
LIBREDESK_PORT=9001
LIBREDESK_CONTAINER_PORT=9000
```

Or inline:

```bash
LIBREDESK_PORT=9001 docker compose up -d
```

- `LIBREDESK_PORT` is the port on the machine, used by the reverse proxy.
- `LIBREDESK_CONTAINER_PORT` is the port inside the container. Leave it at
  `9000` unless you specifically need to change it.
- `LIBREDESK_BIND_ADDR=127.0.0.1` keeps libreDesk off the public interface. Only
  the WAGO webhook needs to be publicly reachable, through the HTTPS proxy.

Compose injects `LIBREDESK_APP__SERVER__ADDRESS` from
`LIBREDESK_CONTAINER_PORT`, so the app and the port mapping always agree.

Then point the reverse proxy at `127.0.0.1:9001`:

```nginx
proxy_pass http://127.0.0.1:9001;
```

## 5. Deploy as a binary with systemd

Build:

```bash
make build
```

`make build`:

1. installs frontend dependencies,
2. builds the main and widget frontends,
3. builds the Go binary,
4. embeds the static assets into `./libredesk` with stuffbin.

Install:

```bash
sudo useradd --system --home /opt/libredesk --shell /usr/sbin/nologin libredesk
sudo mkdir -p /opt/libredesk/uploads
sudo cp libredesk config.toml /opt/libredesk/
sudo chown -R libredesk:libredesk /opt/libredesk

cd /opt/libredesk
sudo -u libredesk ./libredesk --install --yes --config /opt/libredesk/config.toml
sudo -u libredesk ./libredesk --set-system-user-password --config /opt/libredesk/config.toml
```

Create `/etc/systemd/system/libredesk.service`:

```ini
[Unit]
Description=libreDesk (WAGO fork)
After=network-online.target postgresql.service redis-server.service
Wants=network-online.target

[Service]
Type=simple
User=libredesk
Group=libredesk
WorkingDirectory=/opt/libredesk
ExecStart=/opt/libredesk/libredesk --config /opt/libredesk/config.toml
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ReadWritePaths=/opt/libredesk/uploads

[Install]
WantedBy=multi-user.target
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now libredesk
sudo systemctl status libredesk
sudo journalctl -u libredesk -f
```

For a host install behind a reverse proxy, set `[app.server] address` to
`127.0.0.1:9001` (or any free local port) and use that port in the proxy config.

## 6. Reverse proxy and HTTPS

The WAGO webhook needs a public URL. Terminate TLS at Nginx/Caddy and forward to
libreDesk on `127.0.0.1:9000` (or your custom host port).

### Nginx

The examples below use host port `9000`. Replace it with `LIBREDESK_PORT` if you
changed it.

```nginx
server {
    listen 80;
    server_name desk.example.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl http2;
    server_name desk.example.com;

    ssl_certificate     /etc/letsencrypt/live/desk.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/desk.example.com/privkey.pem;

    # Must match app.server.max_body_size.
    client_max_body_size 100m;

    location / {
        proxy_pass http://127.0.0.1:9000;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 300s;
    }

    # Agent websocket.
    location /ws {
        proxy_pass http://127.0.0.1:9000;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # Live chat widget websocket.
    location /widget/ws {
        proxy_pass http://127.0.0.1:9000;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Then issue a certificate, for example with certbot:

```bash
sudo certbot --nginx -d desk.example.com
```

### Set the public root URL

After the first login, go to **Admin → Settings → General → Root URL** and set:

```text
https://desk.example.com
```

This value is used to generate the WAGO webhook URL and public CSAT links. If
libreDesk is mounted under a subpath, include it:

```text
https://desk.example.com/support
```

The generated webhook URL is:

```text
https://desk.example.com/webhooks/whatsapp/<inbox_id>
```

## 7. Configure WAGO

On WAGO:

```bash
WHATSAPP_AUTO_DOWNLOAD_MEDIA=true
WHATSAPP_WEBHOOK_EVENTS=message,message.ack
WHATSAPP_WEBHOOK=https://desk.example.com/webhooks/whatsapp/<inbox_id>
WHATSAPP_WEBHOOK_SECRET=<same as LIBREDESK_WAGO__WEBHOOK_SECRET>
APP_BASIC_AUTH=<user>:<password>
```

Then:

1. Log in to WAGO and connect the single WhatsApp device.
2. Start libreDesk with the matching `[wago]` settings.
3. Create the WhatsApp inbox in **Admin → Inboxes → New inbox → WhatsApp**.
4. Copy the **Callback URL** shown by libreDesk.
5. Put that URL into WAGO's `WHATSAPP_WEBHOOK`.
6. Restart WAGO.
7. Open the inbox and check the **WAGO device** panel: connected and logged in.
8. Send a test WhatsApp message to the device and reply from libreDesk.

Only one WhatsApp inbox may be enabled. Disable any extra WhatsApp inboxes when
upgrading from the old Meta setup.

## 8. Database and Redis

### PostgreSQL backup

```bash
# Docker
docker exec -t libredesk_db pg_dump -U libredesk -Fc libredesk > backup-$(date +%F).dump

# Host install
pg_dump -U libredesk -Fc libredesk > backup-$(date +%F).dump
```

Restore into an empty database:

```bash
pg_restore -U libredesk -d libredesk --clean --if-exists backup-2026-10-24.dump
```

Take a database backup before every `--upgrade` / `docker compose build && up`.

### Redis

Redis holds the durable inbound WhatsApp queue. Use persistence (the compose
file already uses a named volume). If Redis is lost, WAGO retries the webhook,
but jobs already accepted into Redis may need a resend from WAGO.

## 9. Upgrades

### Docker

```bash
git pull
docker compose build --no-cache
docker compose up -d
```

The container runs `--upgrade --yes` before starting the app.

### Binary

```bash
git pull
make build
sudo systemctl stop libredesk
sudo cp libredesk /opt/libredesk/libredesk
sudo chown libredesk:libredesk /opt/libredesk/libredesk
sudo -u libredesk /opt/libredesk/libredesk --upgrade --yes --config /opt/libredesk/config.toml
sudo systemctl start libredesk
```

Keep `config.toml`, `uploads/`, the encryption key, and the database across
upgrades.

## 10. Security checklist

- Use HTTPS for both libreDesk and WAGO; Basic Auth over plain HTTP leaks
  credentials.
- Keep `wago.webhook_secret` / `WHATSAPP_WEBHOOK_SECRET` long and identical on
  both sides. The webhook is rejected when the secret is empty.
- Do not expose PostgreSQL or Redis publicly. The compose file binds them to
  `127.0.0.1`.
- Do not change `app.encryption_key` after installation.
- Protect `config.toml` (it contains DB and WAGO credentials):
  `chmod 600 config.toml`.
- Keep only one WhatsApp inbox enabled.
- Do not put an extra auth layer in front of `/webhooks/whatsapp/*`; WAGO
  authenticates with its HMAC signature.
- Back up the database and `uploads/` regularly.

## 11. Verification checklist

```bash
# libreDesk is up
curl -I https://desk.example.com

# WAGO device is reachable and logged in
curl -u "$WAGO_USER:$WAGO_PASS" https://wago.example.com/devices/<device-id>/status
```

In the UI:

- System/agent login works.
- Admin → Inboxes shows exactly one enabled WhatsApp inbox.
- WAGO device panel says connected and logged in.
- Inbound WhatsApp message creates/reuses a conversation and contact.
- Agent reply arrives on WhatsApp.
- Reply from WhatsApp updates the same conversation.
- CSAT link sends as a normal text message.

## 12. Troubleshooting

| Symptom | Check |
|---|---|
| `wago.base_url and wago.device_id must be set together` | Both `[wago]` values are required. |
| WhatsApp inbox save says WAGO not configured | `LIBREDESK_WAGO__BASE_URL` / `DEVICE_ID` are empty or config.toml is not mounted. |
| `503 wago webhook secret not configured` | Set `LIBREDESK_WAGO__WEBHOOK_SECRET`. |
| `403 invalid signature` | WAGO and libreDesk secrets differ, or a proxy modified the request body. |
| WAGO webhook never arrives | WAGO must reach `https://<root_url>/webhooks/whatsapp/<inbox_id>`. |
| `multiple enabled whatsapp inboxes found` | Disable all but one WhatsApp inbox. |
| Device shows not logged in | Use **Connect with QR** in the inbox panel, or log in from WAGO. |
| Media appears as a placeholder | `WHATSAPP_AUTO_DOWNLOAD_MEDIA=true`, WAGO `statics` reachable, file under the upload cap. |
| Upload fails with 413 | Raise `client_max_body_size` in Nginx and `app.server.max_body_size`. |
| Database errors after update | Run `--upgrade --yes` and check `docker compose logs app` / `journalctl -u libredesk`. |

For WAGO-specific behavior, see [wago-integration.md](wago-integration.md).

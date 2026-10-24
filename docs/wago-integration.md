# WAGO integration

This fork removes libredesk's Meta Cloud API integration and runs the `whatsapp`
channel against a single [WAGO](https://github.com/aldinokemal/go-whatsapp-web-multidevice)
device. WAGO is a separate production service; this repository only contains the
libredesk side.

## What changed

- The `whatsapp` channel now talks to WAGO instead of `graph.facebook.com`.
- One WAGO device is configured globally through `[wago]` / `LIBREDESK_WAGO__*`.
- Exactly **one enabled WhatsApp inbox** is allowed.
- Meta templates, the template picker, and the 24-hour window are removed.
- CSAT is sent as a normal text message with the rating link.
- Inbound messages arrive through WAGO's webhook, and unknown senders create a
  normal editable libredesk contact.
- Group messages, reactions, edits, revokes, and messages sent by the device
  itself (`is_from_me`) are ignored.

## Requirements

- WAGO v8 or newer with multi-device support.
- WAGO must be reachable from libredesk over HTTP(S).
- WAGO media auto-download must stay enabled:
  `WHATSAPP_AUTO_DOWNLOAD_MEDIA=true` (the default).
- LibreDesk must be able to reach WAGO's `statics` files, because inbound media
  is downloaded from `{wago.base_url}/statics/media/...`.
- WAGO must be able to reach LibreDesk's webhook URL.
- Basic Auth is supported and recommended when WAGO is exposed outside a private
  network.

## 1. Configure WAGO

Set these on the WAGO service (environment or CLI flags):

| WAGO setting | Value |
|---|---|
| `WHATSAPP_WEBHOOK` | `https://<libredesk-host>/webhooks/whatsapp/<inbox_id>` |
| `WHATSAPP_WEBHOOK_SECRET` | a long random secret |
| `WHATSAPP_WEBHOOK_EVENTS` | `message,message.ack` (recommended) |
| `WHATSAPP_AUTO_DOWNLOAD_MEDIA` | `true` |
| `APP_BASIC_AUTH` | `<user>:<password>` (or CLI `--basic-auth`) |

Example webhook URL:

```text
https://desk.example.com/webhooks/whatsapp/1
```

`<inbox_id>` is the numeric libredesk inbox ID. In LibreDesk it is shown as the
read-only **Callback URL** in the WhatsApp inbox edit page. The path is always:

```text
/webhooks/whatsapp/<inbox_id>
```

If LibreDesk is mounted under a subpath, put the full public URL in
`app.root_url` (Admin → Settings → General → Root URL), for example:

```text
https://desk.example.com/support
```

Then the generated webhook URL is:

```text
https://desk.example.com/support/webhooks/whatsapp/1
```

WAGO signs every POST with:

```text
X-Hub-Signature-256: sha256=<hmac-sha256-of-raw-body>
```

LibreDesk rejects the request with `403` when the signature is missing or wrong,
and with `503` when WAGO is not configured or the webhook secret is empty.

### Get the WAGO device ID and log in

With Basic Auth:

```bash
curl -u "$WAGO_USER:$WAGO_PASS" https://wago.example.com/devices
```

Use one device ID from the response (`id` or the JID-style ID). To log in, use
the WAGO UI/QR flow, or open the WhatsApp inbox in libreDesk admin and click
**Connect with QR**. LibreDesk starts the QR flow through WAGO's `/app/login`
endpoint with `X-Device-Id`.

## 2. Configure LibreDesk

### Environment variables

```bash
LIBREDESK_WAGO__BASE_URL=https://wago.example.com
LIBREDESK_WAGO__DEVICE_ID=<device-id-from-wago>
LIBREDESK_WAGO__USERNAME=<basic-auth-user>
LIBREDESK_WAGO__PASSWORD=<basic-auth-password>
LIBREDESK_WAGO__WEBHOOK_SECRET=<same value as WHATSAPP_WEBHOOK_SECRET>
```

`BASE_URL` may include a WAGO subpath, for example `https://host/gowa`.
Leave `USERNAME`/`PASSWORD` empty when WAGO has no Basic Auth.

### config.toml

```toml
[wago]
base_url = "https://wago.example.com"
device_id = "<device-id-from-wago>"
username = "<basic-auth-user>"
password = "<basic-auth-password>"
webhook_secret = "<same value as WHATSAPP_WEBHOOK_SECRET>"
```

Environment variables override `config.toml`.

## 3. Create the inbox

1. Start LibreDesk with the WAGO settings above.
2. Go to **Admin → Inboxes → New inbox → WhatsApp**.
3. Fill in the name and, optionally, the CSAT message.
4. Save. The **WAGO device** panel shows the device and its connection state.
5. Copy the **Callback URL** and paste it into WAGO's `WHATSAPP_WEBHOOK`.
6. Restart WAGO so it picks up the webhook and secret.

Only one WhatsApp inbox can be enabled. Creating or enabling a second one is
rejected because LibreDesk is configured with a single WAGO device.

### Upgrading from the Meta integration

Existing `whatsapp` inboxes keep their conversations and contacts. Their old
Meta credential fields are ignored. If more than one WhatsApp inbox is enabled,
disable all but one before sending traffic, otherwise inbound routing fails with
`multiple enabled whatsapp inboxes found`.

## 4. Device management

The inbox edit page has a **WAGO device** panel:

| Action | LibreDesk API | WAGO endpoint |
|---|---|---|
| Read status and device info | `GET /api/v1/wago/device` | `GET /devices/:id` + `/devices/:id/status` |
| Start QR login | `POST /api/v1/wago/device/login` | `GET /app/login` with `X-Device-Id` |
| Reconnect | `POST /api/v1/wago/device/reconnect` | `POST /devices/:id/reconnect` |
| Logout | `POST /api/v1/wago/device/logout` | `POST /devices/:id/logout` |

The QR image is fetched server-side with the WAGO Basic Auth credentials and
returned as a data URL, so the browser never needs WAGO credentials.

WAGO's per-device `/devices/:id/login` endpoint is not implemented in WAGO v8;
LibreDesk therefore uses `/app/login` with `X-Device-Id`.

## 5. Outbound and inbound behavior

### Outbound

- Text: `POST /send/message` with `phone`, `message`, and optional
  `reply_message_id`.
- Image, video, audio, and other files: `POST /send/image`, `/send/video`,
  `/send/audio`, or `/send/file` as multipart.
- One attachment per message. Audio cannot carry a caption.
- WAGO's media endpoints have no reply field, so media replies are sent as plain
  media messages without the quoted reply.
- Read receipts: `POST /message/:id/read` with the contact phone.

### Inbound

- WAGO posts `{event, device_id, payload}` to the webhook.
- `message` events become incoming libredesk messages. The contact identity is
  the phone JID from `chat_id`/`from`; `from_name` becomes the contact name.
- Unknown contacts are created automatically and can be edited in LibreDesk.
- Media uses WAGO's auto-downloaded path (`statics/media/...`). Encrypted
  WhatsApp CDN URLs are not used.
- `message.ack` events update delivery/read status through `payload.ids`.
- Repeated webhook deliveries are deduplicated by the WAGO message ID.

## 6. Media limits

LibreDesk still applies WhatsApp's practical file caps:

| Type | Limit |
|---|---|
| Image | 5 MB |
| Video | 16 MB |
| Audio | 16 MB |
| Document / other | 100 MB |

Incoming media is also capped by the LibreDesk upload limit
(`app.max_file_upload_size`). Files above the cap become a placeholder message.
WAGO auto-download does not preserve the original document filename; libreDesk
stores the generated filename from WAGO's path.

## 7. CSAT

CSAT is sent as a normal WhatsApp text message containing the public rating link.
Configure the optional message in the WhatsApp inbox form. No Meta template is
created, submitted, or approved.

## 8. Docker

The local `docker-compose.yml` builds this fork and does **not** pull
`libredesk/libredesk` from Docker Hub:

```bash
docker compose build
cp config.sample.toml config.toml
# edit [wago] in config.toml
docker compose up -d
```

The image is tagged `libremitu:local`. To rebuild:

```bash
docker compose build --no-cache
docker compose up -d
```

## 9. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `503 wago is not configured` | `LIBREDESK_WAGO__BASE_URL` or `DEVICE_ID` is empty. |
| `503 wago webhook secret not configured` | `LIBREDESK_WAGO__WEBHOOK_SECRET` is empty. |
| `403 invalid signature` | WAGO's `WHATSAPP_WEBHOOK_SECRET` and LibreDesk's `LIBREDESK_WAGO__WEBHOOK_SECRET` differ, or a proxy changed the body. |
| Device panel says not logged in | Click **Connect with QR**, scan, then wait for the status poll to turn green. |
| Send fails with `device not found` | `DEVICE_ID` does not match WAGO's `/devices` response. |
| Media arrives as a placeholder | WAGO auto-download is off, `statics` is unreachable, or the file exceeds the upload cap. |
| Basic Auth failures | Check `USERNAME`/`PASSWORD`; LibreDesk sends them as HTTP Basic Auth. |
| Webhook never arrives | WAGO must be able to reach the public LibreDesk `app.root_url` and the path must be `/webhooks/whatsapp/<inbox_id>`. |

## 10. Limitations

- No Meta templates, no template approval workflow, and no 24-hour message window.
- Only one WAGO device and one enabled WhatsApp inbox are supported.
- Group chats, reactions, edits, revokes, polls, and calls are ignored.
- Media replies do not carry a quoted reply ID.
- WAGO is an unofficial WhatsApp transport. Account bans and WhatsApp policy
  changes are outside LibreDesk's control.
- Upstream libredesk updates need manual merging because this fork removes the
  Meta code paths.

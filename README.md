<a href="https://zerodha.tech"><img src="https://zerodha.tech/static/images/github-badge.svg" align="right" alt="Zerodha Tech Badge" /></a>

<br>
<picture>
  <source
    media="(prefers-color-scheme: dark)"
    srcset="https://s3.ap-south-1.amazonaws.com/libredesk.io/libredesk_white.png?v=2">
  <source
    media="(prefers-color-scheme: light)"
    srcset="https://s3.ap-south-1.amazonaws.com/libredesk.io/libredesk_black.png?v=3">
  <img
    alt="LibreDesk"
    src="https://s3.ap-south-1.amazonaws.com/libredesk.io/libredesk_white.png?v=4"
    width="250">
</picture>

<br> Open source, self-hosted customer support software for email, live chat, and WhatsApp. Distributed as a single binary.

![image](https://libredesk.io/hero-dark.png?v=1)


Visit [libredesk.io](https://libredesk.io) for more info. Check out the [**live demo**](https://demo.libredesk.io/).

> **Custom build:** this fork replaces the Meta Cloud API integration with a single
> [WAGO](https://github.com/aldinokemal/go-whatsapp-web-multidevice) device.
> Read [docs/wago-integration.md](docs/wago-integration.md) before deploying.

## Features

### Inbox and channels

- **Shared inbox:** Handle email, live chat, and WhatsApp conversations in one place.
- **Live chat:** Add a real-time chat widget to your website.
- **WhatsApp:** Connect one WAGO device (single-device mode) instead of the Meta Cloud API.
- **Inbox organization:** Use teams, tags, custom statuses, custom attributes, snoozing, and search.

### Help center and AI

- **Help center:** Publish a searchable, multilingual knowledge base.
- **AI assistant:** Answer live chat questions using your knowledge base and hand conversations to an agent when needed.
- **Agent copilot:** Draft replies, summarize conversations, and find answers without leaving the inbox.

### Workflow and reporting

- **Automations:** Tag, assign, and route conversations using rules you define.
- **Macros:** Send saved replies and apply conversation actions in one step.
- **Auto assignment:** Distribute incoming conversations by agent capacity or your own criteria.
- **SLA management:** Set response targets and get notified about conversations at risk of breaching them.
- **CSAT and analytics:** Collect customer ratings and track response times, resolution rates, and agent activity.

### Administration and integrations

- **Permissions and SSO:** Create roles with per-action permissions and sign in through Google, Microsoft, or another OIDC provider.
- **Activity logs:** Review actions performed by agents and admins.
- **API and webhooks:** Connect LibreDesk to other systems and workflows.

See [libredesk.io](https://libredesk.io) for the full feature set, or try the [live demo](https://demo.libredesk.io/).


## Installation

### Railway (1-click deploy)

The fastest way to get a libredesk instance running. Railway provisions the app, Postgres, and Redis for you.

[![Deploy on Railway](https://railway.com/button.svg)](https://railway.com/deploy/libredesk?referralCode=4gdm5b&utm_medium=integration&utm_source=template&utm_campaign=generic)

__________________

### Docker

This fork builds its own image locally. `docker-compose.yml` uses `build: .` and
never pulls `libredesk/libredesk` from Docker Hub.

```shell
# Build the local image (frontend + Go binary are built inside Docker).
docker compose build

# Copy the sample config and edit it, including the [wago] section.
cp config.sample.toml config.toml

# Run the services in the background.
docker compose up -d

# Set the System user password.
docker exec -it libredesk_app ./libredesk --set-system-user-password
```

The compose file uses the local `libremitu:local` image. To rebuild after code changes,
run `docker compose build --no-cache && docker compose up -d`.
See [docs/wago-integration.md](docs/wago-integration.md) for the WAGO env and webhook setup.

Go to `http://localhost:9000` and login with username `System` and the password you set using the `--set-system-user-password` command.

See [docs/wago-integration.md](docs/wago-integration.md) for the WAGO setup and the local installation notes.

__________________

### Binary
- Download the latest release from your fork's Releases page and extract the libredesk binary.
- Edit config.toml as needed.
- `./libredesk --install` to setup the Postgres DB.
- Run `./libredesk --set-system-user-password` to set the password for the System user.
- Run `./libredesk` and visit `http://localhost:9000` and login with email `System` and the password you set using the --set-system-user-password command.

See [docs/wago-integration.md](docs/wago-integration.md) for the WAGO setup and the local installation notes.
__________________

## Developers

- If you are interested in contributing, **please read [CONTRIBUTING.md](./CONTRIBUTING.md) first**.
- For local development and setup, refer to the [developer setup](https://docs.libredesk.io/contributing/developer-setup).
- For planned features and project direction, see [ROADMAP.md](./ROADMAP.md).

The backend is written in Go and the frontend is Vue.js 3 with Shadcn UI.



## Translators
You can help translate libredesk into your language on [Crowdin](https://crowdin.com/project/libredesk).  
# libremitu

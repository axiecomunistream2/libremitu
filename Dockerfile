# Build the frontend assets.
FROM node:22-alpine AS frontend
RUN corepack enable && corepack prepare pnpm@9.15.3 --activate
WORKDIR /src
COPY frontend/package.json frontend/pnpm-lock.yaml ./frontend/
RUN cd frontend && pnpm install --frozen-lockfile
COPY frontend/ ./frontend
COPY static/ ./static/
COPY internal/countries/countries.json ./internal/countries/countries.json
RUN cd frontend && pnpm build:main && pnpm build:widget && pnpm build:widget-loader

# Build the Go binary and embed the static assets into it.
FROM golang:1.25-alpine AS backend
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/frontend/dist ./frontend/dist
COPY --from=frontend /src/static/widget.min.js ./static/widget.min.js
RUN go install github.com/knadh/stuffbin/...@latest
RUN CGO_ENABLED=0 go build -o /out/libredesk ./cmd
RUN stuffbin -a stuff -in /out/libredesk -out /out/libredesk \
    frontend/dist i18n schema.sql static/email-templates static/public \
    static/widget.min.js:static/widget.js

FROM alpine:3.20
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /libredesk
COPY --from=backend /out/libredesk .
COPY config.sample.toml config.toml
EXPOSE 9000
CMD ["./libredesk"]

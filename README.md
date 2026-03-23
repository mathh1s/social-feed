# social-feed

A tiny social feed with Go backend, Vue.js frontend, SQLite database, and base64 avatar support.

## Run with Docker

```bash
docker compose up --build
```

Open [http://localhost:7291](http://localhost:7291).

Posts persist in a Docker volume (`feed-data`).

## Run locally

```bash
go mod tidy
go run main.go
```

Open [http://localhost:7291](http://localhost:7291).

## Config

| Env var   | Default    | Description          |
|-----------|------------|----------------------|
| `DB_PATH` | `feed.db`  | SQLite database path |
| `ADDR`    | `:7291`    | Listen address       |

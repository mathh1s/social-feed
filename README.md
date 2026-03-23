# social-feed

Tiny social feed. Go + Vue.js + SQLite.

## Features

- Markdown support (bold, italic, code, links, blockquotes)
- Image attachments (resized client-side, stored as base64)
- Link previews (auto-fetched OpenGraph metadata)
- Reply threads (nested replies with inline composer)
- Sound effects (Web Audio API, post sent + new post notification)
- Profile pictures (base64 avatars, cached in localStorage)
- Auto-refresh polling (5s) with "new posts" banner
- Infinite scroll pagination
- Live-updating timestamps

## Run with Docker

```bash
docker compose up --build
```

Open [http://localhost:7291](http://localhost:7291).

## Run locally

```bash
go mod tidy
go run main.go
```

## Structure

```
static/           web assets (html, favicons, manifest)
main.go           server (api, db, link preview, static serving)
Dockerfile        multi-stage build
docker-compose.yml
```

## Config

| Env var   | Default   | Description          |
|-----------|-----------|----------------------|
| `DB_PATH` | `feed.db` | SQLite database path |
| `ADDR`    | `:7291`   | Listen address       |

## API

| Method | Endpoint               | Description                   |
|--------|------------------------|-------------------------------|
| GET    | `/api/posts`           | Paginated top-level posts     |
| POST   | `/api/posts`           | Create post (or reply)        |
| GET    | `/api/posts/new`       | Poll for new posts            |
| GET    | `/api/posts/replies`   | Get replies for a post        |
| GET    | `/api/preview`         | Fetch link preview for a URL  |

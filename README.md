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
main.go            routes, server setup
models.go          structs (Post, Request, Response types)
store.go           SQLite init, migrations, queries
preview.go         link preview fetching, OG tag parsing
validate.go        input validation, rate limiter
middleware.go      CORS, security headers, request logger
helpers.go         JSON response, error, IP, env helpers
static/
  index.html       HTML template
  style.css        all styles
  app.js           Vue app, sounds, markdown
  favicon.*        icons
  site.webmanifest
Dockerfile
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

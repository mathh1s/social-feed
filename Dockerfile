FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/feed .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /bin/feed .
COPY index.html .
COPY favicon.ico favicon.svg favicon-16x16.png favicon-32x32.png ./
COPY apple-touch-icon.png android-chrome-192x192.png android-chrome-512x512.png ./
COPY site.webmanifest .
EXPOSE 7291
VOLUME ["/app/data"]
ENV DB_PATH=/app/data/feed.db
ENTRYPOINT ["./feed"]

FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/feed .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /bin/feed .
COPY static/ static/
EXPOSE 7291
VOLUME ["/app/data"]
ENV DB_PATH=/app/data/feed.db
ENTRYPOINT ["./feed"]

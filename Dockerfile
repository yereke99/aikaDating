FROM node:24-alpine AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/aikabot ./cmd

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && addgroup -S aika && adduser -S -G aika aika
WORKDIR /app
COPY --from=go-builder /out/aikabot /app/aikabot
COPY --from=web-builder /src/web/dist /app/web
RUN mkdir -p /app/data /profile_photo && chown -R aika:aika /app /profile_photo && chmod 0755 /profile_photo
USER aika
ENV APP_ENV=production PORT=8080 WEB_DIR=/app/web DATABASE_PATH=/app/data/aikabot.db PROFILE_PHOTO_DIR=/profile_photo
VOLUME ["/app/data", "/profile_photo"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1:8080/health || exit 1
ENTRYPOINT ["/app/aikabot"]

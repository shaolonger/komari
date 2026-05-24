FROM alpine:3.21

WORKDIR /app

# Docker buildx 会在构建时自动填充这些变量
ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache ca-certificates curl tzdata

RUN addgroup -S -g 10001 komari && adduser -S -u 10001 -G komari komari

COPY komari-${TARGETOS}-${TARGETARCH} /app/komari

RUN chmod +x /app/komari && \
    mkdir -p /app/data && \
    chown -R komari:komari /app

ENV GIN_MODE=release
ENV KOMARI_DB_TYPE=sqlite
ENV KOMARI_DB_FILE=/app/data/komari.db
ENV KOMARI_DB_HOST=localhost
ENV KOMARI_DB_PORT=3306
ENV KOMARI_DB_USER=root
ENV KOMARI_DB_PASS=
ENV KOMARI_DB_NAME=komari
ENV KOMARI_LISTEN=0.0.0.0:25774

EXPOSE 25774

USER komari

CMD ["/app/komari", "server"]

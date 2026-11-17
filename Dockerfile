# Seelynx in one image. See docs/DOCKER.md.

FROM node:26-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.27.3-bookworm AS server
# The version shown in the app. The image has no Git history, so pass it in, such as
# SEELYNX_VERSION=$(git rev-parse --short HEAD) docker compose up -d --build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X seelynx/internal/api.Version=${VERSION}" -o /out/seelynx ./cmd/seelynx

FROM debian:bookworm-slim AS engine
ARG MEDIAMTX_VERSION=v1.21.3
ARG MEDIAMTX_ARCH=linux_amd64
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*
WORKDIR /tmp/mediamtx
RUN set -eu; \
    name="mediamtx_${MEDIAMTX_VERSION}_${MEDIAMTX_ARCH}.tar.gz"; \
    base="https://github.com/bluenviron/mediamtx/releases/download/${MEDIAMTX_VERSION}"; \
    curl -fsSLO "$base/$name"; \
    curl -fsSL "$base/checksums.sha256" | grep -F "*$name" | sha256sum -c -; \
    tar -xzf "$name" mediamtx LICENSE

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates ffmpeg tzdata \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --uid 1000 --create-home seelynx \
 && mkdir -p /data /recordings /usr/share/doc/mediamtx \
 && chown seelynx:seelynx /data /recordings
COPY --from=engine /tmp/mediamtx/mediamtx /usr/local/bin/mediamtx
COPY --from=engine /tmp/mediamtx/LICENSE /usr/share/doc/mediamtx/LICENSE
COPY --from=server /out/seelynx /usr/local/bin/seelynx
COPY LICENSE /usr/share/doc/seelynx/LICENSE
USER seelynx
VOLUME ["/data", "/recordings"]
EXPOSE 8480 8189/udp
ENTRYPOINT ["seelynx", "-data", "/data", "-recordings", "/recordings", "-mediamtx", "/usr/local/bin/mediamtx", "-ffmpeg", "/usr/bin/ffmpeg"]
CMD ["-listen", ":8480"]

# syntax=docker/dockerfile:1.7

# Keep in sync with the `go` directive in go.mod: an older base image either
# forces a GOTOOLCHAIN download during the build or fails outright.
ARG GO_VERSION=1.26
ARG NODE_VERSION=22

FROM node:${NODE_VERSION}-bookworm-slim AS web-build
WORKDIR /src/web

COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:${GO_VERSION}-bookworm AS go-build
ARG APP=server
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The server embeds the production SPA. Replacing this directory during the
# image build prevents a stale local bundle from being shipped accidentally.
RUN rm -rf internal/server/web_dist \
    && mkdir -p internal/server/web_dist \
    && cp -a /src/web/dist/. internal/server/web_dist/
RUN test "$APP" = server || test "$APP" = agent || test "$APP" = client
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
    -o /out/tunnelmesh "./cmd/tunnelmesh-${APP}"

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
ARG APP=server
LABEL org.opencontainers.image.title="TunnelMesh" \
      org.opencontainers.image.description="Authenticated TCP/UDP/HTTP tunnel platform" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.source="https://github.com/nnworld/TunnelMesh"

WORKDIR /var/lib/tunnelmesh
COPY --from=go-build /out/tunnelmesh /usr/local/bin/tunnelmesh

ENV TUNNELMESH_MODE=local \
    TUNNELMESH_STORAGE_DRIVER=sqlite \
    TUNNELMESH_STORAGE_SQLITE_PATH=/var/lib/tunnelmesh/tunnelmesh.db \
    TUNNELMESH_STORAGE_AUTO_INIT=true

VOLUME ["/var/lib/tunnelmesh"]
EXPOSE 80 443
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/tunnelmesh"]
CMD ["run"]

# One image for both modes: `owl agent` and `owl server` (UI embedded).
FROM --platform=$BUILDPLATFORM node:24-alpine AS ui
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# The downloadable CLI (client commands only, -tags owlclient): built once whatever the image's
# platform, gzipped, with checksums of the binaries. Served at /api/v1/cli/<os>-<arch>.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS cli
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p /out/cli && for p in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do \
      CGO_ENABLED=0 GOOS=${p%/*} GOARCH=${p#*/} go build -tags owlclient -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" -o /out/cli/owl-${p%/*}-${p#*/} ./cmd/owl || exit 1; \
    done && cd /out/cli && sha256sum owl-* > SHA256SUMS && gzip -9 owl-*

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /web/dist ./web/dist
# Docs rendered to HTML at build time (the binary carries no Markdown parser).
RUN go run ./internal/docs/docsgen
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/owl ./cmd/owl
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/owl /owl
COPY --from=cli /out/cli /cli
# Server state (SQLite). Mount a volume here; owned by the distroless nonroot user.
COPY --from=build --chown=65532:65532 /out/data /data
ENV OWL_DB=/data/owl.db
USER 65532:65532
# Distroless has no curl: the binary checks /healthz itself (server mode; the agent serves no HTTP,
# and in Kubernetes the pod's own probes apply instead).
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["/owl", "healthcheck"]
ENTRYPOINT ["/owl"]

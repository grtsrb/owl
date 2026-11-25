# Two images from one Dockerfile:
#   --target agent  → owl-agent: the slim agent (-tags owlagent: `owl agent` only — no server, SQLite, UI,
#                     Claude, S3 or CLI code), what every cluster pulls;
#   default (server) → owl: the full binary (server, UI embedded, `owl db …`, CLI downloads; `owl agent` too).
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

# Needs neither the UI nor the docs: the agent build doesn't embed them.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS agent-build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -tags owlagent -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/owl ./cmd/owl

# The agent serves no HTTP, so no HEALTHCHECK and no probes in agent.yaml: Kubernetes restarts it only when it
# exits; a hung agent shows up as the cluster going stale in Owl.
FROM gcr.io/distroless/static-debian12:nonroot AS agent
COPY --from=agent-build /out/owl /owl
USER 65532:65532
ENTRYPOINT ["/owl"]

FROM gcr.io/distroless/static-debian12:nonroot AS server
COPY --from=build /out/owl /owl
COPY --from=cli /out/cli /cli
# Server state (SQLite). Mount a volume here; owned by the distroless nonroot user.
COPY --from=build --chown=65532:65532 /out/data /data
ENV OWL_DB=/data/owl.db
USER 65532:65532
# Distroless has no curl: the binary checks /healthz itself (Docker only; deploy/server sets its own probes).
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["/owl", "healthcheck"]
ENTRYPOINT ["/owl"]

# One image for both modes: `inv agent` and (later) `inv server`.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/inv ./cmd/inv
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/inv /inv
# Server state (SQLite). Mount a volume here; owned by the distroless nonroot user.
COPY --from=build --chown=65532:65532 /out/data /data
ENV INV_DB=/data/inventory.db
USER 65532:65532
ENTRYPOINT ["/inv"]

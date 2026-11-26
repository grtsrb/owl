VERSION  ?= 0.1.0-dev
IMAGE    ?= owl:dev
AGENT_IMAGE ?= owl-agent:dev
CLUSTERS := it-hetzner it-aws-test it-aws-prod
# Different k3s versions per cluster so the demo shows Kubernetes drift/EOL (staging newest, prod oldest).
K3S_it-hetzner  := v1.36.4-k3s1
K3S_it-aws-test := v1.35.0-k3s1
K3S_it-aws-prod := v1.33.6-k3s1

.PHONY: ui ui-dev docs build cli test lint image image-push dev-up dev-down seed agent-print catalog-check agent-deploy stack-up stack-down mattermost-up mattermost-down

## Web UI (web/). `make build` embeds whatever is in web/dist; run `make ui` first for the real UI.
ui:
	cd web && npm ci && npm run build

ui-dev: ## Vite dev server on :5173, proxying /api to the server on :8080
	cd web && npm run dev

## Docs (docs/*.md) rendered to HTML + search index in internal/docs/dist, embedded by `make build`.
docs:
	go run ./internal/docs/docsgen

build: docs
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/owl ./cmd/owl

## The downloadable client (client commands only) for every platform, into bin/cli (the image builds the same).
cli:
	@mkdir -p bin/cli
	@for p in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do \
	  CGO_ENABLED=0 GOOS=$${p%/*} GOARCH=$${p#*/} go build -tags owlclient -trimpath \
	    -ldflags "-s -w -X main.version=$(VERSION)" -o bin/cli/owl-$${p%/*}-$${p#*/} ./cmd/owl || exit 1; \
	done; cd bin/cli && shasum -a 256 owl-* > SHA256SUMS && ls -l

test:
	go test ./...

lint:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

## Both images: the server (owl) and the slim agent (owl-agent, --target agent).
image:
	docker build --pull --build-arg VERSION=$(VERSION) -t $(IMAGE) .
	docker build --pull --build-arg VERSION=$(VERSION) --target agent -t $(AGENT_IMAGE) .

## Multi-arch image (amd64 + arm64, e.g. Graviton) pushed to a registry you're logged in to:
##   make image-push REGISTRY=registry.gitlab.example.com/devops VERSION=0.9.0
image-push:
	@test -n "$(REGISTRY)" || (echo "set REGISTRY, e.g. make image-push REGISTRY=registry.gitlab.example.com/devops VERSION=0.9.0"; exit 1)
	docker buildx build --pull --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) \
	  -t $(REGISTRY)/owl:$(VERSION) --push .
	docker buildx build --pull --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) --target agent \
	  -t $(REGISTRY)/owl-agent:$(VERSION) --push .

## Local dev clusters (k3d). Contexts: k3d-<name>. Does not switch your current kube context.
dev-up:
	@for c in $(CLUSTERS); do \
	  k3d cluster list $$c >/dev/null 2>&1 && { echo "$$c exists"; continue; }; \
	  img=$$(case $$c in it-hetzner) echo $(K3S_it-hetzner);; it-aws-test) echo $(K3S_it-aws-test);; *) echo $(K3S_it-aws-prod);; esac); \
	  k3d cluster create $$c --image rancher/k3s:$$img --servers 1 --agents 0 \
	    --k3s-arg "--disable=traefik@server:0" --k3s-arg "--disable=servicelb@server:0" \
	    --kubeconfig-update-default --kubeconfig-switch-context=false --wait; \
	done
	dev/seed/seed.sh

dev-down:
	k3d cluster delete $(CLUSTERS)

seed:
	dev/seed/seed.sh

## Verify built-in catalog entries against GitHub / endoflife.date / OSV (manual, needs GITHUB_TOKEN in .env):
## make catalog-check [COMPONENTS=traefik,cert-manager]
catalog-check: build
	set -a; [ -f .env ] && . ./.env; set +a; ./bin/owl catalog-check $(if $(COMPONENTS),--component $(COMPONENTS))

## Print what the agent would send for one cluster: make agent-print CLUSTER=it-aws-prod
CLUSTER ?= it-hetzner
agent-print: build
	./bin/owl agent --print --context k3d-$(CLUSTER)

## Server in Docker + an agent in every dev cluster (rotates agent tokens; keeps the DB volume).
stack-up: image
	dev/stack-up.sh

stack-down:
	docker rm -f owl-server owl-s3 owl-mailpit
	@for c in $(CLUSTERS); do kubectl --context k3d-$$c delete -f deploy/agent/agent.yaml --ignore-not-found >/dev/null; done

## Throwaway Mattermost for testing notifications (≈1 GB, amd64 image). Writes dev/.mattermost.env; then `make stack-up`.
mattermost-up:
	dev/mattermost-up.sh

mattermost-down:
	docker rm -f -v owl-mattermost
	rm -f dev/.mattermost.env

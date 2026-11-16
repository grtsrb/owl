VERSION  ?= 0.1.0-dev
IMAGE    ?= owl:dev
CLUSTERS := it-hetzner it-aws-test it-aws-prod
# Different k3s versions per cluster so the demo shows Kubernetes drift/EOL (staging newest, prod oldest).
K3S_it-hetzner  := v1.36.4-k3s1
K3S_it-aws-test := v1.35.0-k3s1
K3S_it-aws-prod := v1.33.6-k3s1

.PHONY: ui ui-dev build test lint image dev-up dev-down seed agent-print agent-deploy stack-up stack-down

## Web UI (web/). `make build` embeds whatever is in web/dist; run `make ui` first for the real UI.
ui:
	cd web && npm ci && npm run build

ui-dev: ## Vite dev server on :5173, proxying /api to the server on :8080
	cd web && npm run dev

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/owl ./cmd/owl

test:
	go test ./...

lint:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

image:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

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

## Print what the agent would send for one cluster: make agent-print CLUSTER=it-aws-prod
CLUSTER ?= it-hetzner
agent-print: build
	./bin/owl agent --print --context k3d-$(CLUSTER)

## Server in Docker + an agent in every dev cluster (rotates agent tokens; keeps the DB volume).
stack-up: image
	dev/stack-up.sh

stack-down:
	docker rm -f owl-server
	@for c in $(CLUSTERS); do kubectl --context k3d-$$c delete -f deploy/agent/agent.yaml --ignore-not-found >/dev/null; done

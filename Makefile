IMG ?= ghcr.io/plate-platform/kovern:latest
LOCAL_IMG ?= kovern:local
HELM_RELEASE ?= kovern
HELM_NAMESPACE ?= kovern-system
ENVTEST_K8S_VERSION ?= 1.33.x

.PHONY: all build test test-integration e2e lint vet fmt tidy \
        docker-build docker-push \
        helm-install helm-uninstall helm-template \
        generate manifests \
        run

all: build

## Build the operator binary
build:
	go build -o bin/kovern ./cmd/main.go

## Run all unit tests (no cluster, no API keys required)
test:
	go test ./internal/... -v -count=1 -race

## Run tests with coverage (unit tests only)
test-coverage:
	go test ./internal/... -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## Run integration tests with envtest (downloads K8s API server + etcd via setup-envtest)
test-integration:
	KUBEBUILDER_ASSETS="$$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@latest use $(ENVTEST_K8S_VERSION) --bin-dir /tmp/envtest-bins -p path)" \
		go test ./tests/integration/... -v -count=1 -timeout=120s

## Run e2e tests against the current cluster context (requires Kovern deployed — make local-deploy)
e2e:
	go test ./tests/e2e/... -v -count=1 -timeout=120s

## Run go vet
vet:
	go vet ./...

## Run gofmt check
fmt:
	@test -z "$$(gofmt -l .)" || (echo "Run 'gofmt -w .' to fix formatting"; gofmt -l .; exit 1)

## Run golangci-lint (install from https://golangci-lint.run)
lint:
	golangci-lint run ./...

## Tidy go.mod
tidy:
	go mod tidy

## Build Docker image
docker-build:
	docker build -t $(IMG) .

## Push Docker image
docker-push:
	docker push $(IMG)

## Install Kovern on the current cluster via Helm (no cert-manager required)
helm-install:
	helm upgrade --install $(HELM_RELEASE) ./charts/kovern \
		--namespace $(HELM_NAMESPACE) \
		--create-namespace \
		--set image.repository=ghcr.io/plate-platform/kovern \
		--set image.tag=latest \
		--wait

## Render chart templates without installing
helm-template:
	helm template $(HELM_RELEASE) ./charts/kovern \
		--namespace $(HELM_NAMESPACE)

## Uninstall Kovern from the current cluster
helm-uninstall:
	helm uninstall $(HELM_RELEASE) --namespace $(HELM_NAMESPACE) || true
	kubectl delete namespace $(HELM_NAMESPACE) --ignore-not-found

## Run the operator locally against the current cluster context (no webhook)
run:
	go run ./cmd/main.go --leader-elect=false

## Generate deep copy functions (requires controller-gen)
generate:
	controller-gen object:headerFile="hack/boilerplate.go.txt" paths="./..."

## Generate CRD manifests (requires controller-gen)
manifests:
	controller-gen crd:trivialVersions=true rbac:roleName=kovern-manager-role \
		webhook paths="./..." output:crd:artifacts:config=charts/kovern/templates

## Build local Docker image (kovern:local) for Docker Desktop
local-image:
	docker build -t $(LOCAL_IMG) .

## Import kovern:local into Docker Desktop's Kubernetes containerd store
local-image-load: local-image
	docker save $(LOCAL_IMG) | docker exec -i desktop-control-plane ctr --namespace=k8s.io images import -

## Generate self-signed certs, create Secret, and deploy on local cluster (Docker Desktop)
local-deploy: local-image-load
	@echo "=== Generating certs and creating Secret ==="
	bash hack/gen-certs.sh $(HELM_NAMESPACE)
	@echo "=== Patching namespace for Helm ownership ==="
	kubectl label namespace $(HELM_NAMESPACE) app.kubernetes.io/managed-by=Helm --overwrite || true
	kubectl annotate namespace $(HELM_NAMESPACE) \
		meta.helm.sh/release-name=$(HELM_RELEASE) \
		meta.helm.sh/release-namespace=$(HELM_NAMESPACE) --overwrite || true
	@echo "=== Installing Helm chart ==="
	bash hack/gen-local-values.sh
	helm upgrade --install $(HELM_RELEASE) ./charts/kovern \
		--namespace $(HELM_NAMESPACE) \
		--create-namespace \
		--values /tmp/kovern-local-values.yaml \
		--wait --timeout=120s

## Remove local deployment
local-clean:
	helm uninstall $(HELM_RELEASE) --namespace $(HELM_NAMESPACE) || true
	kubectl delete namespace $(HELM_NAMESPACE) --ignore-not-found
	rm -rf hack/certs/

## Print help
help:
	@grep -E '^[a-zA-Z_-]+:.*?##' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

# Image registry
REGISTRY        ?= ghcr.io/kapro-dev
OPERATOR_IMG    ?= $(REGISTRY)/kapro-operator:latest

# Tool versions
CONTROLLER_GEN_VERSION ?= v0.17.0
ENVTEST_VERSION        ?= release-0.19
ENVTEST_K8S_VERSION    ?= 1.31.x
GOLANGCI_LINT_VERSION  ?= v2.12.2

# Tool paths
LOCALBIN        ?= $(shell pwd)/bin
CONTROLLER_GEN  ?= $(LOCALBIN)/controller-gen
ENVTEST         ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT   ?= $(LOCALBIN)/golangci-lint

.PHONY: all
all: generate build

##@ General

.PHONY: hooks
hooks: ## Install git pre-commit hook
	cp scripts/pre-commit .git/hooks/pre-commit
	chmod +x .git/hooks/pre-commit
	@echo "✅ Pre-commit hook installed"

.PHONY: help
help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: fmt
fmt: ## Run go fmt
	go fmt ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: $(GOLANGCI_LINT) ## Run golangci-lint
	$(GOLANGCI_LINT) run --timeout 5m

.PHONY: tidy
tidy: ## Run go mod tidy
	go mod tidy

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Generate DeepCopy methods
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: check-deepcopy
check-deepcopy: $(CONTROLLER_GEN) ## Verify zz_generated.deepcopy.go is up to date
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."
	@git diff --exit-code api/v1alpha1/zz_generated.deepcopy.go || \
		(echo "ERROR: zz_generated.deepcopy.go is out of date. Run 'make generate'." && exit 1)

.PHONY: manifests
manifests: $(CONTROLLER_GEN) ## Generate CRD YAML manifests and RBAC
	$(CONTROLLER_GEN) rbac:roleName=kapro-operator "crd:allowDangerousTypes=true" webhook \
		paths="./api/..." paths="./internal/..." paths="./cmd/..." \
		output:crd:artifacts:config=config/crd/bases \
		output:rbac:artifacts:config=config/rbac
	@# controller-gen v0.17 emits an empty `subresources: {}` block for CRDs
	@# without a +kubebuilder:subresource:status marker (Pipeline is
	@# spec-only). Strip it so the install surface matches the Go API intent.
	@for f in config/crd/bases/kapro.io_pipelines.yaml; do \
		sed -i.bak '/^    subresources: {}$$/d' $$f && rm -f $$f.bak; \
	done

.PHONY: test
test: generate manifests $(ENVTEST) ## Run unit + integration tests with envtest
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
		go test ./... -coverprofile cover.out -covermode=atomic
	go tool cover -func cover.out

##@ Build

.PHONY: build
build: generate ## Build operator and CLI binaries
	go build -trimpath -ldflags="-s -w" -o bin/kapro-operator ./cmd/operator
	go build -trimpath -ldflags="-s -w" -o bin/kapro ./cmd/kapro

.PHONY: sync-crds
sync-crds: manifests ## Sync generated CRDs into Helm chart crds/ directory
	cp config/crd/bases/*.yaml charts/kapro-operator/crds/
	@echo "✅ Helm chart CRDs synced"

.PHONY: docker-build
docker-build: ## Build multi-arch Docker image (no push)
	docker buildx build --platform linux/amd64,linux/arm64 -t $(OPERATOR_IMG) -f Dockerfile .

.PHONY: docker-push
docker-push: ## Push Docker image
	docker buildx build --platform linux/amd64,linux/arm64 -t $(OPERATOR_IMG) -f Dockerfile . --push

##@ Cluster

.PHONY: install
install: manifests ## Install CRDs into the active cluster
	kubectl apply -f config/crd/bases

.PHONY: uninstall
uninstall: manifests ## Uninstall CRDs from the active cluster
	kubectl delete --ignore-not-found -f config/crd/bases

.PHONY: deploy
deploy: manifests ## Deploy operator to the active cluster
	kubectl apply -k config/default

.PHONY: undeploy
undeploy: ## Undeploy operator from the active cluster
	kubectl delete --ignore-not-found -k config/default

.PHONY: run
run: generate ## Run operator locally (requires KUBECONFIG)
	go run ./cmd/operator/main.go

##@ Tools

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

$(CONTROLLER_GEN): $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

$(ENVTEST): $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

$(GOLANGCI_LINT): $(LOCALBIN)
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b $(LOCALBIN) $(GOLANGCI_LINT_VERSION)


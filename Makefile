# Image registry
REGISTRY        ?= ghcr.io/kapro-dev
OPERATOR_IMG    ?= $(REGISTRY)/kapro-operator:latest

# Tool versions
CONTROLLER_GEN_VERSION ?= v0.21.0
ENVTEST_VERSION        ?= release-0.19
ENVTEST_K8S_VERSION    ?= 1.31.x
GOLANGCI_LINT_VERSION  ?= v2.12.2
PROTO_FILES            := spec/kai/v1alpha1/actuator.proto spec/kgi/v1alpha1/gate.proto spec/kpi/v1alpha1/planner.proto

# Tool paths
LOCALBIN        ?= $(shell pwd)/bin
CONTROLLER_GEN  ?= $(LOCALBIN)/controller-gen
ENVTEST         ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT   ?= $(LOCALBIN)/golangci-lint

.PHONY: all
all: generate build

##@ General

.PHONY: help
help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: hooks
hooks: ## Install git pre-commit hook (optional, for fast local feedback)
	cp scripts/pre-commit .git/hooks/pre-commit
	chmod +x .git/hooks/pre-commit
	@echo "Pre-commit hook installed. Skip with: git commit --no-verify"

##@ Development

.PHONY: fmt
fmt: ## Auto-fix formatting (gofmt + goimports)
	gofmt -w .
	@which goimports > /dev/null 2>&1 && goimports -w -local kapro.io/kapro . || true

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: verify
verify: validate-yaml-json check-markdown-links fmt vet lint build test ## Run full checks with coverage (use before pushing)

.PHONY: verify-local
verify-local: validate-yaml-json check-markdown-links fmt vet lint build test-no-cover ## Run local checks without coverage tooling

.PHONY: lint
lint: $(GOLANGCI_LINT) ## Run golangci-lint
	$(GOLANGCI_LINT) run --timeout 5m

.PHONY: validate-yaml-json
validate-yaml-json: ## Validate CI, example, monitoring YAML and Grafana dashboard JSON
	scripts/validate-yaml-json

.PHONY: check-markdown-links
check-markdown-links: ## Check local links in README/docs/examples/monitoring Markdown
	python3 scripts/check-markdown-links.py

.PHONY: tidy
tidy: ## Run go mod tidy
	go mod tidy

.PHONY: proto
proto: ## Generate Go stubs from KAI/KGI/KPI proto contracts
	@if command -v buf >/dev/null 2>&1; then \
		buf generate; \
	else \
		command -v protoc >/dev/null 2>&1 || { echo "protoc is required when buf is not installed"; exit 1; }; \
		command -v protoc-gen-go >/dev/null 2>&1 || { echo "protoc-gen-go is required"; exit 1; }; \
		command -v protoc-gen-go-grpc >/dev/null 2>&1 || { echo "protoc-gen-go-grpc is required"; exit 1; }; \
		protoc -I . \
			--go_out=. --go_opt=paths=source_relative \
			--go-grpc_out=. --go-grpc_opt=paths=source_relative \
			$(PROTO_FILES); \
	fi

.PHONY: check-proto
check-proto: proto ## Verify generated proto stubs are up to date
	@git diff --exit-code -- spec/kai/v1alpha1 spec/kgi/v1alpha1 spec/kpi/v1alpha1 || \
		(echo "ERROR: generated proto stubs are out of date. Run 'make proto'." && exit 1)

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Generate DeepCopy methods
	$(CONTROLLER_GEN) object:headerFile="build/boilerplate.go.txt" paths="./..."

.PHONY: check-deepcopy
check-deepcopy: $(CONTROLLER_GEN) ## Verify zz_generated.deepcopy.go is up to date
	$(CONTROLLER_GEN) object:headerFile="build/boilerplate.go.txt" paths="./..."
	@git diff --exit-code api/v1alpha1/zz_generated.deepcopy.go || \
		(echo "ERROR: zz_generated.deepcopy.go is out of date. Run 'make generate'." && exit 1)

.PHONY: manifests
manifests: $(CONTROLLER_GEN) ## Generate CRD YAML manifests and RBAC
	$(CONTROLLER_GEN) rbac:roleName=kapro-operator "crd:allowDangerousTypes=true" webhook \
		paths="./api/..." paths="./internal/..." paths="./cmd/..." \
		output:crd:artifacts:config=config/crd/bases \
		output:rbac:artifacts:config=config/rbac
	@# controller-gen v0.17 emits an empty `subresources: {}` block for CRDs
	@# without a +kubebuilder:subresource:status marker (PromotionPlan is
	@# spec-only). Strip it so the install surface matches the Go API intent.
	@for f in config/crd/bases/kapro.io_promotionplans.yaml; do \
		sed -i.bak '/^    subresources: {}$$/d' $$f && rm -f $$f.bak; \
	done

.PHONY: test-no-cover
test-no-cover: generate manifests $(ENVTEST) ## Run unit + integration tests with envtest, without coverage
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
		go test ./...

.PHONY: test
test: generate manifests $(ENVTEST) ## Run unit + integration tests with envtest and coverage
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
		go test ./... -coverprofile cover.out -covermode=atomic
	go tool cover -func cover.out

##@ Build

.PHONY: build
build: generate ## Build operator and CLI binaries
	go build -trimpath -ldflags="-s -w" -o bin/kapro-operator ./cmd/operator
	go build -trimpath -ldflags="-s -w" -o bin/kapro ./cmd/kapro

.PHONY: release-smoke
release-smoke: ## Smoke-test Helm packaging and release workflow chart artifacts
	scripts/ci-release-smoke.sh

.PHONY: sync-crds
sync-crds: manifests ## Sync generated CRDs into Helm chart crds/ and internal/bootstrap/kaprocrds/ (used by hub init)
	cp config/crd/bases/*.yaml charts/kapro-operator/crds/
	cp config/crd/bases/*.yaml internal/bootstrap/kaprocrds/
	@echo "✅ Helm chart and bootstrap CRDs synced"

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

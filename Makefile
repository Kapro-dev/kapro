IMG ?= ghcr.io/kapro-io/kapro:latest
CONTROLLER_GEN ?= $(shell which controller-gen 2>/dev/null || echo "go run sigs.k8s.io/controller-tools/cmd/controller-gen")

.PHONY: all generate manifests build docker-build fmt vet tidy

all: generate build

# Generate DeepCopy + CRD YAML from kubebuilder markers
generate:
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

manifests:
	$(CONTROLLER_GEN) rbac:roleName=kapro-operator crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

# Build Go binaries
build: generate
	go build -o bin/kapro-operator ./cmd/operator
	go build -o bin/kapro-cluster-controller ./cmd/cluster-controller

# Docker image
docker-build:
	docker buildx build --platform linux/amd64,linux/arm64 -t $(IMG) . --push

# Code quality
fmt:
	go fmt ./...

vet:
	go vet ./...

tidy:
	go mod tidy

# Install CRDs into active cluster
install: manifests
	kubectl apply -f config/crd/bases

# Uninstall CRDs from active cluster
uninstall: manifests
	kubectl delete -f config/crd/bases

# Run the operator locally (needs KUBECONFIG set)
run: generate
	go run ./cmd/operator/main.go

.PHONY: build test test-race vet fmt lint check battery ci-local ci-docker

# Versiones fijadas del toolchain de CI (deben coincidir con .github/workflows/ci.yml)
GO_VERSION := 1.26.5
LINT_VERSION := v2.12.2

# Compila todos los paquetes.
build:
	go build ./...

# Tests unitarios (sin Ollama: la batería vive tras el build tag `ollama`).
test:
	go test ./... -count=1

# Tests con detector de carreras.
test-race:
	go test -race ./... -count=1

vet:
	go vet ./...

# Verifica que no queden archivos sin gofmt.
fmt:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "Archivos sin gofmt:"; echo "$$unformatted"; exit 1; \
	fi

lint:
	golangci-lint run --timeout=5m

# Puerta completa local antes de pushear.
check: fmt vet test-race lint

# Corre solo la batería de validación contra Ollama (requiere el modelo cargado).
# El build tag `ollama` es obligatorio: sin él la batería ni se compila, y con él
# la ausencia de Ollama es un fallo (antes se saltaba sola y nadie se enteraba).
battery:
	go test -tags ollama ./classifier -run TestBattery -count=1 -v

# --- Red local de CI (réplica de .github/workflows/ci.yml) ---

# Pre-push: agregado de gates locales antes de mergear (== check + build).
ci-local: fmt vet lint test-race build

# Simula el CI en Docker (Go $(GO_VERSION) + golangci-lint $(LINT_VERSION)) — requiere Docker.
ci-docker:
	@docker run --rm \
		-e GOFLAGS=-buildvcs=false \
		-v "$$(go env GOPATH)/pkg/mod:/go/pkg/mod" \
		-v "$(CURDIR):/workspace" -w /workspace \
		golang:$(GO_VERSION)-bookworm \
		bash -c "set -e; curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b /usr/local/bin $(LINT_VERSION) && make ci-local"

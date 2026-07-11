.PHONY: build test test-race vet fmt lint check battery

# Compila todos los paquetes.
build:
	go build ./...

# Tests unitarios (la batería contra Ollama real se salta sola si no hay modelo).
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
battery:
	go test ./classifier -run TestBattery -count=1 -v

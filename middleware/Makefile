GO            := $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
BRAIN         := rotor-brain
CLI           := rotor
export GOTOOLCHAIN := local
VERSION := $(shell git -C .. describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION) -s -w"

# Build targets
.PHONY: build build-windows build-pi build-pi32 all clean run

# Linux x86-64 (primary)
build:
	GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o bin/$(BRAIN) ./cmd/brain
	GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o bin/$(CLI)   ./cmd/rotor

# Windows x86-64
build-windows:
	GOOS=windows GOARCH=amd64 $(GO) build $(LDFLAGS) -o bin/$(BRAIN).exe ./cmd/brain
	GOOS=windows GOARCH=amd64 $(GO) build $(LDFLAGS) -o bin/$(CLI).exe   ./cmd/rotor

# Raspberry Pi (arm64 — Pi 3/4/5 running 64-bit OS)
build-pi:
	GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o bin/$(BRAIN)-pi ./cmd/brain
	GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o bin/$(CLI)-pi   ./cmd/rotor

# Raspberry Pi 2 / Zero W (arm32)
build-pi32:
	GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(LDFLAGS) -o bin/$(BRAIN)-pi32 ./cmd/brain
	GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(LDFLAGS) -o bin/$(CLI)-pi32   ./cmd/rotor

# All platforms
all: build build-windows build-pi

run: build
	./bin/$(BRAIN)

clean:
	rm -rf bin/

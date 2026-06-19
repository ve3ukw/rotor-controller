GO            := $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
BRAIN         := rotor-brain
CLI           := rotor
HAMLIB        := rotor-hamlib
SERIALSHIM    := rotor-serial
export GOTOOLCHAIN := local
VERSION := $(shell git -C .. describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION) -s -w"

# Build targets
.PHONY: build build-windows build-pi build-pi32 all clean run

# Linux x86-64 (primary)
build:
	GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o bin/$(BRAIN)      ./cmd/brain
	GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o bin/$(CLI)        ./cmd/rotor
	GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o bin/$(HAMLIB)     ./cmd/hamlib
	GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o bin/$(SERIALSHIM) ./cmd/serial

# Windows x86-64
build-windows:
	GOOS=windows GOARCH=amd64 $(GO) build $(LDFLAGS) -o bin/$(BRAIN).exe      ./cmd/brain
	GOOS=windows GOARCH=amd64 $(GO) build $(LDFLAGS) -o bin/$(CLI).exe        ./cmd/rotor
	GOOS=windows GOARCH=amd64 $(GO) build $(LDFLAGS) -o bin/$(HAMLIB).exe     ./cmd/hamlib
	GOOS=windows GOARCH=amd64 $(GO) build $(LDFLAGS) -o bin/$(SERIALSHIM).exe ./cmd/serial

# Raspberry Pi (arm64 — Pi 3/4/5 running 64-bit OS)
build-pi:
	GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o bin/$(BRAIN)-pi      ./cmd/brain
	GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o bin/$(CLI)-pi        ./cmd/rotor
	GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o bin/$(HAMLIB)-pi     ./cmd/hamlib
	GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o bin/$(SERIALSHIM)-pi ./cmd/serial

# Raspberry Pi 2 / Zero W (arm32)
build-pi32:
	GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(LDFLAGS) -o bin/$(BRAIN)-pi32      ./cmd/brain
	GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(LDFLAGS) -o bin/$(CLI)-pi32        ./cmd/rotor
	GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(LDFLAGS) -o bin/$(HAMLIB)-pi32     ./cmd/hamlib
	GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(LDFLAGS) -o bin/$(SERIALSHIM)-pi32 ./cmd/serial

# All platforms
all: build build-windows build-pi

run: build
	./bin/$(BRAIN)

clean:
	rm -rf bin/

# apsnav Makefile
#
# The APS client_id is injected at build time so it never appears in source.
# Store your client_id in a local .aps-client-id file (git-ignored), or set
# the CLIENT_ID variable directly:
#
#   make build CLIENT_ID=your-client-id
#   make install CLIENT_ID=your-client-id

CLIENT_ID  ?= $(shell cat .aps-client-id 2>/dev/null | tr -d '[:space:]')
REGION     ?= $(shell cat .aps-region 2>/dev/null | tr -d '[:space:]')
MODULE     := github.com/schneik80/FusionDataCLI
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS    := -X $(MODULE)/config.DefaultClientID=$(CLIENT_ID) \
              -X $(MODULE)/config.DefaultRegion=$(REGION) \
              -X main.version=$(VERSION)

.PHONY: build install clean check

build:
	@[ -n "$(CLIENT_ID)" ] || (echo "ERROR: CLIENT_ID is not set. See Makefile header." && exit 1)
	go build -ldflags "$(LDFLAGS)" -o fusiondatacli .

install:
	@[ -n "$(CLIENT_ID)" ] || (echo "ERROR: CLIENT_ID is not set. See Makefile header." && exit 1)
	go install -ldflags "$(LDFLAGS)" .

# Build without an embedded client_id — for local dev using env vars or config.json
dev:
	go build -o fusiondatacli .

clean:
	rm -f fusiondatacli

check:
	go vet ./...
	go test ./...

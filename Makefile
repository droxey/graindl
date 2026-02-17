VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS   = -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)"

.PHONY: build test vet lint clean docker verify

build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o graindl .

test:
	go test -count=1 -race ./...

vet:
	go vet ./...

lint: vet
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed — skipping"

verify:
	go mod verify

clean:
	rm -f graindl

docker:
	docker build -t graindl:$(VERSION) .

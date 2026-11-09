GO ?= go
CONFIG ?= configs/config.yaml

.PHONY: setup test lint race run build inspect integration
setup:
	$(GO) mod download
build:
	$(GO) build -o bin/sentinel ./cmd/sentinel
test:
	$(GO) test -timeout 120s ./...
lint:
	@test -z "$$($(GO)fmt -l cmd internal migrations prompts)" || (printf 'Run gofmt and review the changes\n'; exit 1)
	$(GO) vet ./...
race:
	$(GO) test -race -timeout 180s ./...
run:
	$(GO) run ./cmd/sentinel run --config $(CONFIG)
inspect:
	$(GO) run ./cmd/sentinel inspect --file testdata/telegram_updates/promotion.json
integration:
	@test -n "$(TEST_DATABASE_URL)" || (printf 'Set TEST_DATABASE_URL to a disposable PostgreSQL database\n'; exit 1)
	$(GO) test -race -count=1 -timeout 180s ./internal/app

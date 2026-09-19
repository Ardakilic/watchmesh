GO := golang:1.24.13-bookworm
RUN := docker run --rm -v $(PWD):/src -w /src $(GO)
MIGRATE := migrate/migrate:v4.20.1

.PHONY: build dev test itest cover clean migrate-new

build:
	docker build -t watchmesh .

dev:
	docker compose up --build

test:
	$(RUN) go test ./... -count=1

itest:
	docker compose -f compose.test.yml up --abort-on-container-exit --exit-code-from tester

cover:
	$(RUN) go test -coverprofile=c.out ./internal/... -count=1 && $(RUN) go tool cover -func=c.out | awk '/^total:/ {print; if ($$3+0 < 90) {print "coverage below 90%" > "/dev/stderr"; exit 1}}'

clean:
	rm -f c.out watchmesh
	docker compose down -v

migrate-new:
	docker run --rm -v $(PWD)/migrations:/migrations $(MIGRATE) create -ext sql -dir /migrations -seq $(NAME)

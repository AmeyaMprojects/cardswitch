.PHONY: up down logs smoke test test-go test-java load dup-test

GO_SERVICES := gateway auth-switch issuer-sim
JAVA_SERVICES := tokenization-vault ledger settlement

up:
	docker compose up -d --build

down:
	docker compose down -v

logs:
	docker compose logs -f

smoke:
	./scripts/smoke.sh

test: test-go test-java

test-go:
	@for s in $(GO_SERVICES); do \
		echo "== $$s"; \
		docker run --rm -v "$(CURDIR)/services/$$s":/s -w /s golang:1.22 go test -race ./... || exit 1; \
	done

test-java:
	@for s in $(JAVA_SERVICES); do \
		echo "== $$s"; \
		docker run --rm -v "$(CURDIR)/services/$$s":/s -w /s maven:3.9-eclipse-temurin-21 mvn -q -B test || exit 1; \
	done

load:
	docker run --rm -i --network host -e BASE_URL=http://localhost:8080 -e RATE=$(or $(RATE),100) -e DURATION=$(or $(DURATION),60s) grafana/k6:0.54.0 run - < loadtest/charge.js

dup-test:
	docker run --rm -i --network host -e BASE_URL=http://localhost:8080 -e RUN=$$RANDOM grafana/k6:0.54.0 run - < loadtest/duplicate_retries.js

.PHONY: up down logs test-go load dup-test

up:
	docker compose up -d --build

down:
	docker compose down -v

logs:
	docker compose logs -f

test-go:
	cd services/auth-switch && go test ./...

load:
	docker run --rm -i --network host -e BASE_URL=http://localhost:8080 grafana/k6 run - < loadtest/charge.js

dup-test:
	docker run --rm -i --network host -e BASE_URL=http://localhost:8080 grafana/k6 run - < loadtest/duplicate_retries.js

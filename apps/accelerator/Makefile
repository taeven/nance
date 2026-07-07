.PHONY: build build-proxy run run-proxy dev-up dev-down seed test test-integration lint

export NANCE_MASTER_KEY ?= MTIzNDU2Nzg5MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMQo=

build:
	go build -o bin/controlplane ./cmd/controlplane

build-proxy:
	go build -o bin/proxy ./cmd/proxy

build-all: build build-proxy

run: build
	./bin/controlplane

# Data-plane proxy (Phase 1). Listens on :27018 by default (compose Mongo owns :27017).
run-proxy: build-proxy
	./bin/proxy

# Start local infra (Postgres + Mongo + Redis)
dev-up:
	docker compose up -d --wait
	@echo "Postgres, Mongo and Redis are up."
	@echo "Start control plane: make run"
	@echo "Start proxy:         make run-proxy"
	@echo "Then seed:           make seed"

dev-down:
	docker compose down -v

# Seed a demo tenant + connection + policy + proxy access token
# Requires the control plane to be running on :8080 and NANCE_ADMIN_TOKEN (or none in dev)
seed:
	@echo "Creating demo tenant..."
	curl -s -X POST http://localhost:8080/api/v1/tenants \
		-H "Content-Type: application/json" \
		-H "Authorization: Bearer $${NANCE_ADMIN_TOKEN:-dev}" \
		-d '{"id":"demo","name":"Demo Tenant"}' | cat

	@echo "\nCreating default connection (pointing at the compose mongo)..."
	@CONN_JSON=$$(curl -s -X POST http://localhost:8080/api/v1/tenants/demo/connections \
		-H "Content-Type: application/json" \
		-H "Authorization: Bearer $${NANCE_ADMIN_TOKEN:-dev}" \
		-d '{"name":"default","uri":"mongodb://root:example@localhost:27017/?authSource=admin"}'); \
	echo "$$CONN_JSON" | cat; \
	CONN_ID=$$(echo "$$CONN_JSON" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); \
	if [ -z "$$CONN_ID" ]; then echo "Failed to create connection"; exit 1; fi; \
	echo "\nTesting connection $$CONN_ID..."; \
	curl -s -X POST "http://localhost:8080/api/v1/tenants/demo/connections/$$CONN_ID/test" \
		-H "Authorization: Bearer $${NANCE_ADMIN_TOKEN:-dev}" | cat; \
	echo "\nSetting a sample cache policy on the connection..."; \
	curl -s -X PUT "http://localhost:8080/api/v1/tenants/demo/connections/$$CONN_ID/policy/collections/mydb.users" \
		-H "Content-Type: application/json" \
		-H "Authorization: Bearer $${NANCE_ADMIN_TOKEN:-dev}" \
		-d '{"enabled":true,"ttlSeconds":300,"maxResultBytes":1048576}' | cat; \
	echo "\nIssuing proxy access (copy proxyConnectionUri / rawToken!):"; \
	curl -s -X POST "http://localhost:8080/api/v1/tenants/demo/connections/$$CONN_ID/tokens" \
		-H "Content-Type: application/json" \
		-H "Authorization: Bearer $${NANCE_ADMIN_TOKEN:-dev}" \
		-d '{"description":"local dev token"}' | cat

	@echo "\n\nDone. Prefer the proxyConnectionUri from issue response, or PLAIN auth:"
	@echo '  mongodb://demo:<rawToken>@127.0.0.1:27018/?authMechanism=PLAIN&authSource=$$external'

test:
	go test ./...

# Live proxy + cache test against localhost Mongo (:27017) and Redis (:6379).
# Requires: make dev-up (mongo + redis). Starts an ephemeral proxy in-process.
test-integration:
	go test -tags=integration -count=1 ./internal/proxy/integrationtest/ -v -timeout 2m

lint:
	go vet ./...
	gofmt -l .

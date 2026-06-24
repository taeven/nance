.PHONY: build build-proxy run run-proxy dev-up dev-down seed test test-integration lint

export NANCE_MASTER_KEY ?= thisisexactly32byteslongforaes256!!

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

# Seed a demo tenant + backend + policy + token
# Requires the control plane to be running on :8080 and NANCE_ADMIN_TOKEN (or none in dev)
seed:
	@echo "Creating demo tenant..."
	curl -s -X POST http://localhost:8080/api/v1/tenants \
		-H "Content-Type: application/json" \
		-H "Authorization: Bearer $${NANCE_ADMIN_TOKEN:-dev}" \
		-d '{"id":"demo","name":"Demo Tenant"}' | cat

	@echo "\nSetting backend (pointing at the compose mongo)..."
	curl -s -X POST http://localhost:8080/api/v1/tenants/demo/backend \
		-H "Content-Type: application/json" \
		-H "Authorization: Bearer $${NANCE_ADMIN_TOKEN:-dev}" \
		-d '{"uri":"mongodb://root:example@localhost:27017/?authSource=admin"}' | cat

	@echo "\nTesting the backend connection..."
	curl -s -X POST http://localhost:8080/api/v1/tenants/demo/backend/test \
		-H "Authorization: Bearer $${NANCE_ADMIN_TOKEN:-dev}" | cat

	@echo "\nSetting a sample cache policy..."
	curl -s -X PUT http://localhost:8080/api/v1/tenants/demo/policy/collections/mydb.users \
		-H "Content-Type: application/json" \
		-H "Authorization: Bearer $${NANCE_ADMIN_TOKEN:-dev}" \
		-d '{"enabled":true,"ttlSeconds":300,"maxResultBytes":1048576}' | cat

	@echo "\nIssuing a data-plane token (copy the rawToken!):"
	curl -s -X POST http://localhost:8080/api/v1/tenants/demo/tokens \
		-H "Content-Type: application/json" \
		-H "Authorization: Bearer $${NANCE_ADMIN_TOKEN:-dev}" \
		-d '{"description":"local dev token"}' | cat

	@echo "\n\nDone. Connect through the proxy (default :27018) with PLAIN auth:"
	@echo '  mongodb://demo:<rawToken>@127.0.0.1:27018/mydb?authMechanism=PLAIN&authSource=$$external'
	@echo "Or with the nance scheme (drivers treat it like mongodb:// if you rewrite the scheme):"
	@echo '  mongodb+nance://demo:<rawToken>@127.0.0.1:27018/mydb?authMechanism=PLAIN&authSource=$$external'

test:
	go test ./...

test-integration:
	go test -tags=integration -count=1 ./...

lint:
	go vet ./...
	gofmt -l .

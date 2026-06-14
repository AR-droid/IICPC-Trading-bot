.PHONY: build up down logs clean test

build:
	docker-compose build

up:
	docker-compose up -d

down:
	docker-compose down

logs:
	docker-compose logs -f

clean:
	docker-compose down -v
	# Remove any dangling contestant containers
	docker ps -a --filter "name=iicpc-contestant-" -q | xargs -r docker rm -f

test:
	@echo "Running verification tests..."
	cd tests && go test -v ./... || true

test-docker:
	@echo "Running tests inside Docker (no local Go required)..."
	docker run --rm --network iicpc-trading-hackathon_benchmarking-net \
		-v "$(PWD)/tests:/app" \
		-w /app \
		-e TEST_TARGET_URL=http://iicpc-contestant-team1:8080 \
		golang:1.22-alpine \
		go test -v ./... || true

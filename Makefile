.DEFAULT_GOAL := test

.PHONY: test race lint build web-build docker-build release

test:
	go test ./...

race:
	go test -race ./...

lint:
	go vet ./...

build:
	go build ./cmd/...

web-build:
	@if [ -f web/package.json ]; then \
		cd web && npm run build; \
	else \
		echo "web application is not present yet; skipping web build"; \
	fi

docker-build:
	docker build --build-arg APP=server -t tunnelmesh:server .
	docker build --build-arg APP=agent -t tunnelmesh:agent .
	docker build --build-arg APP=client -t tunnelmesh:client .

release:
	VERSION="$(VERSION)" ./scripts/build-release.sh

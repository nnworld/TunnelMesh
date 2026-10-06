.DEFAULT_GOAL := test

.PHONY: test race lint build web-build tray-web-build docker-build release tray-release release-with-tray

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

# The tray's settings bundle is embedded at compile time and is a separate front end from
# the admin console, so it has its own build step. `make release` deliberately does not call
# it: the tray links Cocoa and WebKit through cgo and can only be compiled on macOS, while
# release cross-compiles everything else with CGO_ENABLED=0. The release workflow packages it
# in a separate macOS job and merges the .dmg files in through TRAY_DIST_DIR.
tray-web-build:
	cd web-tray && npm run build

docker-build:
	docker build --build-arg APP=server -t tunnelmesh:server .
	docker build --build-arg APP=agent -t tunnelmesh:agent .
	docker build --build-arg APP=client -t tunnelmesh:client .

release:
	VERSION="$(VERSION)" ./scripts/build-release.sh

# macOS host only. Run `make tray-web-build` first, or the script refuses to package an app
# whose window would render a placeholder page.
tray-release:
	VERSION="$(VERSION)" ./scripts/package-macos-tray.sh

# macOS host only: the whole release in one command, i.e. what the workflow's two build jobs
# produce together. Publishes six platform archives plus two tray .dmg files under one
# SHA256SUMS. On Linux run `make release` and let the workflow supply the tray artifacts.
release-with-tray: tray-release
	VERSION="$(VERSION)" TRAY_DIST_DIR="dist/$(VERSION)/macos-tray" ./scripts/build-release.sh

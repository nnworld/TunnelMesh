.DEFAULT_GOAL := test

.PHONY: test race lint build web-build tray-web-build docker-build release tray-release tray-windows-release release-with-tray release-with-trays

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
# it: the macOS tray links Cocoa and WebKit through cgo and can only be compiled on macOS,
# while release cross-compiles everything else with CGO_ENABLED=0. The Windows tray is pure Go
# but still belongs to its own packaging step, because that is where the icon / version /
# manifest resources and the NSIS installer are produced. The release workflow packages both in
# separate jobs and merges them through TRAY_DIST_DIR and WINDOWS_TRAY_DIST_DIR.
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

# Any host with go, zip and makensis: the Windows tray is cross-compiled, so this does not need
# a Windows machine - only NSIS, which `apt-get install nsis` provides.
tray-windows-release:
	VERSION="$(VERSION)" ./scripts/package-windows-tray.sh

# macOS host only: the whole release in one command, i.e. what the workflow's two build jobs
# produce together. Publishes six platform archives plus two tray .dmg files under one
# SHA256SUMS. On Linux run `make release` and let the workflow supply the tray artifacts.
release-with-tray: tray-release
	VERSION="$(VERSION)" TRAY_DIST_DIR="dist/$(VERSION)/macos-tray" ./scripts/build-release.sh

# Both tray platforms merged into one release, i.e. what the workflow's three build jobs
# produce together: six platform archives, two .dmg, two green windows archives and one
# windows installer, all under a single SHA256SUMS.
release-with-trays: tray-release tray-windows-release
	VERSION="$(VERSION)" \
	  TRAY_DIST_DIR="dist/$(VERSION)/macos-tray" \
	  WINDOWS_TRAY_DIST_DIR="dist/$(VERSION)/windows-tray" \
	  ./scripts/build-release.sh

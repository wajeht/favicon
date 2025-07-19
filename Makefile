dev:
	@go run github.com/cosmtrek/air@v1.43.0 \
		--build.cmd "make build" --build.bin "/tmp/bin/favicon" --build.delay "100" \
		--build.exclude_dir "" \
		--build.include_ext "go, tpl, tmpl, html, css, scss, js, ts, sql, jpeg, jpg, gif, png, bmp, svg, webp, ico, md" \
		--misc.clean_on_exit "true"

build:
	@go build -o /tmp/bin/favicon main.go

commit:
	@git add -A
	@git auto

push:
	@go test ./...
	@go fmt ./...
	@git add -A
	@git auto
	@git push --no-verify

test:
	@go test ./...

format:
	@go mod tidy -v
	@go fmt ./...

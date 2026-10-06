# Go vendoring blueprint (builds without internet access)

Why: the CI environment that builds our images has no internet access, so a build must not download anything.
Cost (fusion-wizard): `vendor/` 57 MB on disk, ~7 MB compressed in git, 4,324 files.

## 1. Vendor the dependencies
```
go mod tidy && go mod vendor
```
Go uses `vendor/` automatically when `go.mod` says `go 1.14` or later; `-mod=vendor` makes it explicit.

## 2. `.gitignore`
Remove any `vendor/` line, otherwise the files never reach git.

## 3. `.gitattributes` (keeps reviews and language stats clean)
```
vendor/** -diff linguist-vendored
```

## 4. Dockerfile
Replace `go mod download` with a copy of `vendor/` and build with `-mod=vendor`:
```dockerfile
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY ...                      # source dirs
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -a -o <bin> ./cmd/
```

## 5. Makefile
```make
.PHONY: vendor check-vendor

## Refresh vendor/ from go.mod.
vendor:
	go mod tidy
	go mod vendor

## Fail when vendor/ drifted from go.mod/go.sum.
check-vendor:
	go mod vendor
	git diff --exit-code -- vendor go.mod go.sum
	@test -z "$$(git ls-files --others --exclude-standard -- vendor)" || (echo "untracked files in vendor/" && exit 1)
```
Also build and test with `-mod=vendor` (`go build -mod=vendor ...`, `go test -mod=vendor ./... -race`).

## 6. Docs
Add a CHANGELOG entry and a note in the project CLAUDE.md: vendored because CI has no internet,
run `make vendor` after any `go.mod` change, commit `vendor/` together with `go.mod`/`go.sum`.

## 7. Verify
```
docker build --network none -t <name>:<fresh-tag> .
docker run --rm --entrypoint /<bin> <name>:<fresh-tag> --help
```
`--network none` fails on any hidden download. Run `make check-vendor` in CI.

## What vendoring does NOT cover
- **Docker base images** (e.g. `golang:1.25-alpine`, `gcr.io/distroless/static:nonroot`): CI must pull them from an
  internal mirror, or load them with `docker save` / `docker load`.
- **Go toolchain outside Docker**: if `go.mod` needs a newer Go than installed, `GOTOOLCHAIN=auto` tries to
  download it. Install the right version and set `GOTOOLCHAIN=local` so a mismatch fails clearly.
- **Tools run via `go run` / `go install`** (e.g. controller-gen) are not vendored unless listed as tool
  dependencies; keep them out of the CI build path or install them in the image.

## Ongoing rule
Every dependency change: `make vendor`, then commit `vendor/`, `go.mod` and `go.sum` together.

# https://github.com/casey/just

[private]
default:
    @just --list

build:
    go build -o registry ./cmd/registry

run *args:
    go run ./cmd/registry {{ args }}

fmt:
    go fmt ./...
    go vet ./...

check:
    @test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
    go vet ./...

test: fmt
    go test -race ./...

install-hook:
    @printf '#!/bin/sh\nset -e\njust check\n' > .git/hooks/pre-commit
    @chmod +x .git/hooks/pre-commit

remove-hook:
    @rm .git/hooks/pre-commit

# `just add-tag 0.1.0`
add-tag VERSION:
    #!/usr/bin/env bash
    set -euo pipefail
    VERSION="{{ VERSION }}"
    git push origin main
    git tag -a "v${VERSION}" -m "Release v${VERSION}"
    git push origin "v${VERSION}"

# `just remove-tag v0.0.0` or `just remove-tag` (uses fzf)
remove-tag VERSION="":
    #!/usr/bin/env bash
    set -euo pipefail
    tag="{{ VERSION }}"
    [ -z "$tag" ] && tag=$(git tag | sort -V | fzf --prompt="Select tag to remove: ")
    [ -z "$tag" ] && echo "No tag selected" && exit 1
    git tag -d "$tag"
    git push --delete origin "$tag"

# Undo last commit locally and on remote, keeping changes staged.
undo-commit:
    #!/usr/bin/env bash
    set -euo pipefail

    upstream=$(git rev-parse --abbrev-ref --symbolic-full-name '@{u}')
    remote="${upstream%%/*}"
    branch="${upstream#*/}"

    git reset --soft HEAD~1
    git push --force-with-lease "$remote" "HEAD:$branch"

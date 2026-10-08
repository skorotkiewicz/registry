# Crate and npm registry

A small self-hosted registry written in Go, with no external dependencies. Use normal `cargo publish`, `npm publish`, and dependency installs. Package metadata and archives live on disk.

All package reads and writes require one shared token. Only health checks and Cargo's registry configuration are public. Anyone with the token can publish under any name or yank any crate. Use it for trusted publishers, not as a public multi-user registry.

## Run

Requires Go 1.23 or newer to build. The compiled server needs no Go installation.

```sh
go build -o registry .
export REGISTRY_TOKEN="$(openssl rand -hex 32)"
export PUBLIC_URL=http://localhost:8080
./registry
```

Keep the token somewhere safe. Reuse it after restarting, and set the same token in your clients. The server refuses to start with a token shorter than 16 characters or containing whitespace.

| Variable | Default | Purpose |
| --- | --- | --- |
| `REGISTRY_TOKEN` | Required | Shared read/write token |
| `PUBLIC_URL` | `http://localhost:8080` | Client-visible origin, without a path |
| `LISTEN_ADDR` | `127.0.0.1:8080` | HTTP bind address |
| `DATA_DIR` | `data` | Persistent storage directory |

For another machine, set `PUBLIC_URL` to its actual URL. For example, `https://packages.example.com`. Put the server behind an HTTPS reverse proxy before using it outside localhost. The proxy must forward `/cargo/` and `/npm/`, allow 64 MiB request bodies, and preserve the `Authorization` header. The server does not trust forwarded headers to construct download URLs.

### Docker

```sh
docker build -t own-registry .
export REGISTRY_TOKEN="$(openssl rand -hex 32)"
docker run -d --name own-registry \
  -p 127.0.0.1:8080:8080 \
  -e REGISTRY_TOKEN \
  -e PUBLIC_URL=http://localhost:8080 \
  -v own-registry-data:/data \
  --restart unless-stopped \
  own-registry
```

The container runs as UID 65532. A named volume stores the packages. If you use a bind mount instead, make its directory writable by that UID. Keep `PUBLIC_URL` consistent with the address used by clients, including its scheme and port.

## Cargo

Requires Cargo 1.74 or newer for this authenticated sparse-index setup.

Add this to `~/.cargo/config.toml` or your project's `.cargo/config.toml`:

```toml
[registries.selfhost]
index = "sparse+http://localhost:8080/cargo/"
credential-provider = "cargo:token"
```

Use the server's token in the client shell:

```sh
export CARGO_REGISTRIES_SELFHOST_TOKEN="$REGISTRY_TOKEN"
cargo publish --registry selfhost
```

Alternatively, `cargo login --registry selfhost` stores the token in Cargo's credentials file.

In the crate being published, restrict publication to this registry:

```toml
[package]
name = "my-library"
version = "0.1.0"
publish = ["selfhost"]
```

In a consuming project's `Cargo.toml`:

```toml
[dependencies]
my-library = { version = "0.1.0", registry = "selfhost" }
```

```sh
cargo build
cargo yank my-library --version 0.1.0 --registry selfhost
cargo yank my-library --version 0.1.0 --undo --registry selfhost
```

The registry supports renamed dependencies, optional dependencies, extended feature syntax, and dependencies from other registries. Crate names must start with a lowercase letter and contain only lowercase letters, digits, hyphens, or underscores, up to 64 characters. Names differing only by hyphens and underscores cannot coexist.

## npm

Use a scope so normal public npm packages still come from npmjs.org. Add to your project's `.npmrc`:

```ini
@selfhost:registry=http://localhost:8080/npm/
//localhost:8080/npm/:_authToken=${REGISTRY_TOKEN}
```

Export the actual server token in your client shell. Do not commit it to `.npmrc`.

For a package with `"name": "@selfhost/my-package"` in `package.json`:

```sh
npm publish --registry http://localhost:8080/npm/
npm install @selfhost/my-package --no-audit
```

Publishing with `--tag beta` preserves `latest` and adds a `beta` tag. Regular and scoped names both work. For an unscoped package, use `--registry http://localhost:8080/npm/` on both publish and install, and configure authentication for that URL.

Replace both URLs in `.npmrc` when using another host. For HTTPS, the auth key still omits the scheme, for example `//packages.example.com/npm/:_authToken=${REGISTRY_TOKEN}`.

## Storage and limits

- Published versions cannot be overwritten. Cargo yank changes availability without deleting the archive.
- Uploads commit the archive first and then atomically replace metadata. An interrupted publish can leave an unused archive or temporary file, but not a partial committed archive.
- Run exactly one server process per data directory. Requests share one lock. This is intended for small team registries, not high-throughput hosting.
- Requests are limited to 64 MiB. npm's base64 encoding means its tarball limit is about 48 MiB, less metadata overhead. Uploads and package metadata are buffered in memory.
- Stop the server before backing up the entire data directory for a consistent backup. Restore it with the same file permissions. The token is not stored in that directory.
- No upstream proxy, public package mirror, user accounts, ownership API, search, npm login, npm audit, npm unpublish, standalone dist-tag editing, or provenance bundles. Configure tokens directly and use `--no-audit` for npm installs. Archives are stored without extracting or inspecting their manifests. Only publish packages you trust.

## Tests

```sh
go test -race ./...
go vet ./...
bash smoke.sh
```

The smoke test requires Go, Cargo, npm, Node, curl, and Bash. It starts a localhost server, publishes and consumes real packages, checks duplicate rejection and Cargo yank/undo, then restarts the server to check persistence. Test files stay in the printed temporary directory. Set `SMOKE_PORT` if port 18080 is occupied.

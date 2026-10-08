#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
port=${SMOKE_PORT:-18080}
export REGISTRY_TOKEN=smoke-test-token-not-for-production
export PUBLIC_URL="http://127.0.0.1:$port"
private=${SMOKE_PRIVATE:-true}
case "$private" in true|false) ;; *) echo 'SMOKE_PRIVATE must be true or false'; exit 1 ;; esac
cat > "$work/server.toml" <<EOF
listen_addr = "127.0.0.1:$port"
public_url = "$PUBLIC_URL"
data_dir = "$work/data"
private = $private
[[users]]
nick = "smokepublisher"
token = "$REGISTRY_TOKEN"
[[users]]
nick = "second-user"
token = "second-smoke-test-token"
EOF
export CARGO_HOME="$work/cargo-home"
export CARGO_REGISTRIES_SELFHOST_TOKEN="$REGISTRY_TOKEN"
export npm_config_cache="$work/npm-cache"
export npm_config_userconfig="$work/npmrc"
printf 'registry=%s/npm/\n//127.0.0.1:%s/npm/:_authToken=%s\naudit=false\nfund=false\n' "$PUBLIC_URL" "$port" "$REGISTRY_TOKEN" > "$npm_config_userconfig"
mkdir -p "$CARGO_HOME"
cat > "$CARGO_HOME/config.toml" <<EOF
[registries.selfhost]
index = "sparse+$PUBLIC_URL/cargo/"
credential-provider = "cargo:token"
EOF
cd "$root"
go build -ldflags="-X main.version=$(cat VERSION)" -o "$work/crateyard" ./cmd/crateyard
[[ "$("$work/crateyard" --version)" == "crateyard $(cat VERSION)" ]]
"$work/crateyard" -config "$work/server.toml" > "$work/server.log" 2>&1 &
pid=$!
trap 'kill "$pid" 2>/dev/null || true; printf "Test files: %s\n" "$work"' EXIT
ready=false
for _ in {1..100}; do
  if ! kill -0 "$pid" 2>/dev/null; then cat "$work/server.log"; exit 1; fi
  if curl -fsS "$PUBLIC_URL/healthz" > /dev/null; then ready=true; break; fi
  sleep 0.1
done
if [[ "$ready" != true ]]; then cat "$work/server.log"; exit 1; fi

mkdir -p "$work/npm-package" "$work/npm-consumer"
cd "$work/npm-package"
printf '{"name":"@selfhost/smoke","version":"1.0.0","main":"index.js"}\n' > package.json
printf 'module.exports = 42;\n' > index.js
npm publish --ignore-scripts
if npm publish --ignore-scripts > "$work/npm-duplicate.log" 2>&1; then echo 'duplicate npm version accepted'; exit 1; fi
node -e 'let p=require("./package.json");p.version="2.0.0-beta.1";require("node:fs").writeFileSync("package.json",JSON.stringify(p))'
npm publish --tag beta --ignore-scripts
[[ -f "$work/data/npm/smokepublisher/@selfhost%2Fsmoke/metadata.json" ]]
[[ -f "$work/data/npm/smokepublisher/@selfhost%2Fsmoke/1.0.0.tgz" ]]
[[ ! -e "$work/data/npm/@selfhost%2Fsmoke" ]]
cd "$work/npm-consumer"
printf '{"name":"consumer","version":"1.0.0","private":true}\n' > package.json
if [[ "$private" == false ]]; then
  printf 'registry=%s/npm/\n' "$PUBLIC_URL" > "$work/npm-anonymousrc"
  npm --userconfig "$work/npm-anonymousrc" install @selfhost/smoke --ignore-scripts --no-audit
else
  npm install @selfhost/smoke --ignore-scripts --no-audit
fi
node -e 'const assert=require("node:assert/strict");assert.equal(require("@selfhost/smoke"),42);assert.equal(require("@selfhost/smoke/package.json").version,"1.0.0")'

# Publish a crate, then a crate with a renamed optional dependency and features2.
mkdir -p "$work/rust-leaf/src" "$work/rust-top/src" "$work/rust-consumer/src"
cd "$work/rust-leaf"
cat > Cargo.toml <<'EOF'
[package]
name = "smoke-leaf"
version = "0.1.0"
edition = "2021"
description = "Registry smoke test"
license = "MIT"
EOF
printf 'pub fn answer() -> u32 { 42 }\n' > src/lib.rs
cargo publish --registry selfhost --allow-dirty
if cargo publish --registry selfhost --allow-dirty > "$work/cargo-duplicate.log" 2>&1; then echo 'duplicate crate version accepted'; exit 1; fi
cd "$work/rust-top"
cat > Cargo.toml <<'EOF'
[package]
name = "smoke-top"
version = "0.1.0"
edition = "2021"
description = "Registry smoke test with renamed dependency"
license = "MIT"
[dependencies]
leaf_alias = { package = "smoke-leaf", version = "0.1.0", registry = "selfhost", optional = true }
[features]
default = ["answer"]
answer = ["dep:leaf_alias"]
EOF
printf 'pub fn answer() -> u32 { leaf_alias::answer() }\n' > src/lib.rs
cargo publish --registry selfhost --allow-dirty
# Use a fresh Cargo cache to force real index and archive downloads.
export CARGO_HOME="$work/cargo-consumer-home"
mkdir -p "$CARGO_HOME"
cp "$work/cargo-home/config.toml" "$CARGO_HOME/config.toml"
cd "$work/rust-consumer"
cat > Cargo.toml <<'EOF'
[package]
name = "consumer"
version = "0.1.0"
edition = "2021"
[dependencies]
smoke-top = { version = "0.1.0", registry = "selfhost" }
EOF
printf 'fn main() { assert_eq!(smoke_top::answer(), 42); }\n' > src/main.rs
if [[ "$private" == false ]]; then
  env -u CARGO_REGISTRIES_SELFHOST_TOKEN cargo run
else
  cargo run
fi
cargo yank smoke-top --version 0.1.0 --registry selfhost
cargo yank smoke-top --version 0.1.0 --undo --registry selfhost
# Restart to verify that the registry does not depend on memory state.
kill "$pid"
wait "$pid" 2>/dev/null || true
"$work/crateyard" -config "$work/server.toml" >> "$work/server.log" 2>&1 &
pid=$!
for _ in {1..100}; do
  if curl -fsS "$PUBLIC_URL/healthz" > /dev/null 2>&1; then break; fi
  sleep 0.1
done
npm view @selfhost/smoke version | grep -Fx '1.0.0'
curl -fsS -H "Authorization: $REGISTRY_TOKEN" "$PUBLIC_URL/cargo/sm/ok/smoke-top" | grep -q '"vers":"0.1.0"'
curl -fsS "$PUBLIC_URL/" | grep -q '<h1>Crateyard</h1>'
curl -fsS -H "Authorization: Bearer $REGISTRY_TOKEN" "$PUBLIC_URL/api/packages" | node -e '
let body = "";
process.stdin.on("data", chunk => body += chunk);
process.stdin.on("end", () => {
  const assert = require("node:assert/strict");
  const { packages } = JSON.parse(body);
  assert.equal(packages.length, 3);
  assert.ok(packages.every(p => p.publisher === "smokepublisher"));
  assert.equal(packages.find(p => p.name === "@selfhost/smoke").versions.length, 2);
  assert.equal(packages.find(p => p.name === "smoke-top").versions[0].yanked, false);
});'
curl -fsS -H 'Authorization: Bearer second-smoke-test-token' "$PUBLIC_URL/npm/-/whoami" | grep -q '"username":"second-user"'
if [[ "$private" == false ]]; then
  curl -fsS "$PUBLIC_URL/api/packages" > /dev/null
  status=$(curl -sS -o /dev/null -w '%{http_code}' -X DELETE "$PUBLIC_URL/cargo/api/v1/crates/smoke-top/0.1.0/yank")
  [[ "$status" == 401 ]]
fi
echo "Cargo/npm publish/install, users, duplicate rejection, yank/undo, restart, and web catalog passed; private=$private."

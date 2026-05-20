# envoy-mini-builder

Build Envoy on a remote Mac mini and publish the binary as a GitHub release asset.

SSHes to your Mac mini, runs a Bazel build, streams logs live, downloads the
result via SFTP, and publishes it to a GitHub release — all from a single command.

## Install

```sh
brew install dio/tap/envoy-mini-builder
```

Or download a binary from the [releases page](https://github.com/dio/envoy-mini-builder/releases).

## Usage

```
envoy-mini-builder build --sha <ref> [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--sha` | *(required)* | Commit SHA, branch, or tag |
| `--repo` | `envoyproxy/envoy` | Source repo (`owner/repo`); forks work |
| `--patch` | | Raw URL to a `.patch` file applied before build |
| `--tag` | `envoy-{sha8}-{date}` | Override release tag |
| `--no-release` | `false` | Build only, skip release create/upload |
| `--out` | `./dist` | Local directory for the downloaded binary |
| `--suffix` | | Suffix appended to binary name (e.g. `-patched`) |
| `--no-strip` | `false` | Skip post-build strip (useful for symbol analysis) |
| `--host` | `dio@mini` | SSH host of the Mac mini |
| `--port` | `22` | SSH port |
| `--jobs` | `HOST_CPUS` | Bazel `--jobs` value |
| `--gh-repo` | `dio/envoy-builder` | GitHub repo for release assets |
| `--bb-key` | `$BUILDBUDDY_API_KEY` | BuildBuddy API key (remote cache) |

### Examples

```sh
# Minimal: build main, publish to dio/envoy-builder
GITHUB_TOKEN=$(gh auth token) envoy-mini-builder build --sha main

# Fork + patch + custom tag
envoy-mini-builder build \
  --repo  your-org/envoy \
  --sha   a1b2c3d4 \
  --patch https://gist.githubusercontent.com/dio/.../my.patch \
  --tag   envoy-my-fix-20260519

# Build only, no release
envoy-mini-builder build --sha main --no-release --out ./dist

# Custom SSH host
envoy-mini-builder build --sha main --host user@192.168.1.10 --port 2222
```

### Environment variables

| Variable | Description |
|----------|-------------|
| `GITHUB_TOKEN` | Required for release operations (skip with `--no-release`) |
| `BUILDBUDDY_API_KEY` | BuildBuddy API key for remote cache on mini |

## Dynamic module symbol export (macOS)

On macOS, Envoy is compiled with `-fvisibility=hidden`. Strong `extern "C"` callback
functions are NOT placed in the Mach-O export trie, so `strip` removes them and
`dlsym` cannot resolve them at runtime. This is a macOS-specific issue — on Linux,
`-rdynamic` exports all global symbols automatically.

Two fixes are available:

### Fix 2 — visibility patch (preferred)

Apply a source patch that adds `#pragma GCC visibility push(default)` around the
two `extern "C"` blocks that define `envoy_dynamic_module_callback_*` symbols:

```sh
go run . build \
  --sha  $SHA \
  --repo envoyproxy/envoy \
  --patch https://gist.github.com/dio/c642501419c3513a7d6e992c8b146f93/raw/dynamic-module-export-fix.patch \
  --no-release --out ./dist
```

The patch targets:
- `source/extensions/dynamic_modules/abi_impl.cc`
- `source/extensions/filters/http/dynamic_modules/abi_impl.cc`

This produces a `Clean` version string (patch is applied before build, workspace
`.bazelrc` is never touched).

### Fix 1 — linker exported-symbol wildcard (no source changes)

Pass a linker flag via `--bazel-arg` to force all matching symbols into the export
trie at link time, without modifying source:

```sh
go run . build \
  --sha  $SHA \
  --repo envoyproxy/envoy \
  "--bazel-arg=--linkopt=-Wl,-exported_symbol,_envoy_dynamic_module_callback_*" \
  --no-release --out ./dist
```

Note the leading underscore: macOS linker requires C symbol names to be prefixed
with `_` in `-exported_symbol` patterns.

## Auth

The CLI authenticates to the Mac mini using your local **ssh-agent** (checked
first) or `~/.ssh/id_ed25519` / `~/.ssh/id_rsa` / `~/.ssh/id_ecdsa` as fallbacks.
No passwords. No key copying. Your agent handles it.

`GITHUB_TOKEN` is only needed locally to create/publish the GitHub release.
It is never forwarded to the mini.

`BUILDBUDDY_API_KEY` is forwarded to the mini as a process environment variable
(via the script prologue piped to `bash -s` stdin). During the build it is
written to `.bazelrc.cache` in the workspace so Bazel can read it; a `trap`
deletes that file on script exit regardless of success or failure. It is never
written to the shell history or any persistent config file on mini.

## How it works

1. Creates a **draft** GitHub release
2. SSHes to the mini, pipes the build script to `bash -s`
3. Streams all build logs to your terminal in real-time
4. Remote script emits a `BINARY_PATH:…` sentinel on stdout
5. Downloads the binary over **SFTP** (same SSH connection, no extra auth)
6. Uploads the binary as a release asset
7. Publishes the release

On failure the draft is marked `[FAILED]` as a prerelease so you don't lose partial work.

The mini keeps its workspace at `~/envoy-builder/{repo}/src/` between runs.
Bazel's local disk cache accumulates there, cutting subsequent builds significantly.

## Building from source

```sh
git clone https://github.com/dio/envoy-mini-builder
cd envoy-mini-builder
go build -o envoy-mini-builder .
```

## Releases

goreleaser publishes binaries for:

| OS | Arch |
|----|------|
| macOS | arm64 |
| Linux | amd64 |
| Linux | arm64 |

The Homebrew formula in [dio/homebrew-tap](https://github.com/dio/homebrew-tap)
is updated automatically on each release.

## License

MIT

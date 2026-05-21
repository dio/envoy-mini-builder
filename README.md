# envoy-mini-builder

Build Envoy on a remote Mac mini and publish the binary as a GitHub release asset.

SSHes to your Mac mini, runs a Bazel build, streams logs live, downloads the
result via SFTP, and publishes it to a GitHub release — all from a single command.

## Requirements

- **`ssh`** and **`scp`** (standard on macOS)
- **[`gh`](https://cli.github.com/)** — GitHub CLI, authenticated (`gh auth login`); used for release operations

```sh
brew install gh
gh auth login
```

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
| `--tag` | `envoy-{sha8}` | Override release tag; e.g. `envoy-abcdef12-patched` for variants |
| `--no-release` | `false` | Build only, skip release create/upload |
| `--force-build` | `false` | Always rebuild even if the asset already exists in the release |
| `--out` | `./dist` | Local directory for the downloaded binary |
| `--suffix` | | Suffix appended to binary/asset name (e.g. `-patched`) |
| `--no-strip` | `false` | Skip post-build `strip -x` (useful for symbol analysis) |
| `--platform` | `macos-arm64` | Target platform: `macos-arm64` \| `linux-arm64` \| `linux-amd64` |
| `--all-platforms` | `false` | Build all supported platforms sequentially under one release |
| `--host` | `dio@mini` | SSH host of the Mac mini |
| `--port` | `22` | SSH port |
| `--jobs` | `HOST_CPUS` | Bazel `--jobs` value |
| `--bazel-arg` | | Extra Bazel flag; repeatable; prefix `platform:` to scope |
| `--gh-repo` | `dio/envoy-builder` | GitHub repo for release assets |
| `--bb-key` | | BuildBuddy API key (all platforms); see env vars below |

### Examples

```sh
# Minimal: build main, publish to dio/envoy-builder
envoy-mini-builder build --sha main

# Build all platforms under one release tag
envoy-mini-builder build --sha main --all-platforms

# Fork + patch + custom tag
envoy-mini-builder build \
  --repo   your-org/envoy \
  --sha    a1b2c3d4 \
  --patch  https://gist.githubusercontent.com/dio/.../my.patch \
  --suffix -patched \
  --tag    envoy-a1b2c3d4-patched

# Build only, no release
envoy-mini-builder build --sha main --no-release --out ./dist

# Custom SSH host
envoy-mini-builder build --sha main --host user@192.168.1.10 --port 2222
```

### Environment variables

| Variable | Description |
|----------|-------------|
| `BUILDBUDDY_API_KEY_MACOS_ARM64` | BuildBuddy key for macOS arm64 builds |
| `BUILDBUDDY_API_KEY_LINUX_ARM64` | BuildBuddy key for Linux arm64 builds |
| `BUILDBUDDY_API_KEY_LINUX_AMD64` | BuildBuddy key for Linux amd64 builds |
| `BUILDBUDDY_API_KEY` | Fallback BuildBuddy key (used when platform-specific var is unset) |

GitHub auth is handled by `gh` — run `gh auth login` once. No `GITHUB_TOKEN` needed.

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

**Mac mini** — uses your local **ssh-agent** or `~/.ssh/id_{ed25519,rsa,ecdsa}`.
No passwords. No key copying.

**GitHub** — uses `gh` CLI. Run `gh auth login` once; the token is never
forwarded to the mini.

**BuildBuddy** — the API key is forwarded to the mini as a process environment
variable via the script prologue. During the build it is written to
`.bazelrc.cache` outside the workspace; a `trap` deletes it on exit regardless
of success or failure. Never written to shell history or any persistent config.

## How it works

1. Creates a **draft** GitHub release via `gh release create`
2. SSHes to the mini, pipes the build script to `bash -s`
3. Streams all build logs to your terminal in real-time
4. Remote script emits a `BINARY_PATH:…` sentinel on stdout
5. Downloads the binary via `scp`
6. Uploads the binary as a release asset via `gh release upload`
7. Publishes the release via `gh release edit --draft=false`

On failure the draft is marked `[FAILED]` as a prerelease so you don't lose partial work.

## Tag and variant scheme

| Scenario | `--sha` | `--tag` | `--suffix` | Asset name |
|----------|---------|---------|------------|------------|
| Clean build | `abc123ef` | *(auto: `envoy-abc123ef`)* | | `envoy-macos-arm64` |
| With patch | `abc123ef` | `envoy-abc123ef-patched` | `-patched` | `envoy-macos-arm64-patched` |
| Custom Bazel flags | `abc123ef` | `envoy-abc123ef-custom` | `-custom` | `envoy-macos-arm64-custom` |
| All platforms | `abc123ef` | *(auto)* | | `envoy-{platform}` |

The default tag `envoy-{sha8}` gives one canonical release per Envoy commit.
Use `--tag` + `--suffix` together for patch/variant builds to keep both the
release and the asset names unambiguous.

### Default: download if exists, build if not

The default behavior checks the release for each platform's asset before
building. If the asset already exists it is downloaded; only missing assets
trigger a remote build. Use `--force-build` to always rebuild:

```sh
# Downloads existing asset if found, otherwise builds
envoy-mini-builder build --sha abc123ef

# Force rebuild even if the asset is already published
envoy-mini-builder build --sha abc123ef --force-build
```

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

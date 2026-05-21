# envoy-mini-builder

Build Envoy on a remote Mac mini and publish the binary as a GitHub release asset.

SSHes to your Mac mini, runs a Bazel build, streams logs live, downloads the result via `scp`, and publishes it to a GitHub release — all from a single command. Linux builds run inside OrbStack VMs on the mini.

## Requirements

- **`ssh`** and **`scp`** (standard on macOS/Linux)
- **[`gh`](https://cli.github.com/)** — GitHub CLI, authenticated

```sh
brew install gh
gh auth login
```

- **[OrbStack](https://orbstack.dev/)** on the Mac mini — required for Linux builds; macOS builds run natively

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
| `--tag` | `envoy-{sha8}` | Override release tag; required for variant builds |
| `--no-release` | `false` | Build only, skip release create/upload |
| `--force-build` | `false` | Always rebuild even if the asset already exists |
| `--out` | `./dist` | Local directory for the downloaded binary |
| `--suffix` | | Suffix appended to binary/asset name (e.g. `-patched`) |
| `--no-strip` | `false` | Skip post-build `strip -x` |
| `--platform` | `macos-arm64` | Target: `macos-arm64` \| `linux-arm64` \| `linux-amd64` |
| `--all-platforms` | `false` | Build all supported platforms under one release |
| `--host` | `dio@mini` | SSH host of the Mac mini |
| `--port` | `22` | SSH port |
| `--jobs` | `HOST_CPUS` | Bazel `--jobs` value |
| `--bazel-arg` | | Extra Bazel flag; repeatable; prefix `platform:` to scope (e.g. `linux-arm64:--flag`) |
| `--gh-repo` | `dio/envoy-builder` | GitHub repo for release assets |
| `--bb-key` | | BuildBuddy API key; see env vars below |
| `--detach` | `false` | Start build in background on the mini; use `jobs`/`logs`/`fetch` to manage |

### Examples

```sh
# Minimal: build main, publish to dio/envoy-builder
envoy-mini-builder build --sha main

# All platforms under one release tag
envoy-mini-builder build --sha main --all-platforms

# Detached: start builds in background, check later
envoy-mini-builder build --sha main --all-platforms --detach
envoy-mini-builder jobs
envoy-mini-builder fetch envoy-abc123ef

# Fork + patch + custom tag
envoy-mini-builder build \
  --repo   your-org/envoy \
  --sha    a1b2c3d4 \
  --patch  https://raw.githubusercontent.com/.../my.patch \
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
| `BUILDBUDDY_API_KEY` | Fallback key used when no platform-specific var is set |

## Detached builds

`--detach` starts the build in the background on the mini and returns immediately.

```sh
# Start
envoy-mini-builder build --sha main --all-platforms --detach

# Check status
envoy-mini-builder jobs

# Tail logs for a specific platform
envoy-mini-builder logs envoy-abc123ef --platform linux-arm64

# Download and publish once done
envoy-mini-builder fetch envoy-abc123ef

# Cancel and clean up
envoy-mini-builder cancel envoy-abc123ef
```

`fetch` creates the GitHub release, uploads binaries, and publishes it — then removes local job state.

## Download-or-build

For each platform the CLI first checks the release for an existing asset. If found it is downloaded; its `.params.json` sidecar is verified to match current build params. Only missing or mismatched assets trigger a remote build. Use `--force-build` to always rebuild.

The auto tag `envoy-{sha8}` is reserved for default-param builds (`envoyproxy/envoy`, no patch, no extra Bazel args, no `--no-strip`). Variant builds must supply `--tag` explicitly — the CLI errors otherwise.

## How it works

**macOS builds**

1. SSH to the mini, pipe the build script to `bash -s`
2. Stream build logs live; capture `BINARY_PATH:…` sentinel from stdout
3. Download the binary via `scp`
4. Upload to GitHub release via `gh`

**Linux builds**

Same, but the build script runs inside an OrbStack VM via `orb run -m <machine>`. The binary lives inside the VM, so after the build it is staged on the Mac mini host with `orb cp` before the `scp` download.

## Auth

**Mac mini** — uses your local ssh-agent or `~/.ssh/id_{ed25519,rsa,ecdsa}`. No passwords.

**GitHub** — uses `gh` CLI. Run `gh auth login` once; the token is never forwarded to the mini.

**BuildBuddy** — the API key is forwarded to the mini as a process environment variable, written to `.bazelrc.cache` outside the workspace, and deleted on script exit via `trap`. Never written to shell history or any persistent config.

## Building from source

```sh
git clone https://github.com/dio/envoy-mini-builder
cd envoy-mini-builder
go build -o envoy-mini-builder .
```

## License

MIT

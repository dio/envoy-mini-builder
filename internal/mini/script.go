package mini

// remoteScript is the body of the bash script that runs on the Mac mini.
// It is concatenated after a shell-safe prologue (see buildPrologue) that
// exports all user-supplied values as properly quoted variables:
//
//	ENVOY_REPO, COMMIT_SHA, PATCH_URL, BAZEL_JOBS, BUILDBUDDY_API_KEY
//	BAZEL_EXTRA_ARGS
//
// The combined string is piped to `bash -s` over SSH stdin.
// All build logs go to stdout/stderr; the single sentinel line
//
//	BINARY_PATH:/abs/path/to/envoy
//
// is emitted to stdout for the Go caller to extract.
const remoteScriptDarwin = `
set -euo pipefail
PATH="/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:$PATH"
export PATH

echo "→ host: $(hostname) $(uname -m) macOS $(sw_vers -productVersion)"

# ── bootstrap ─────────────────────────────────────────────────────────────────
BREW=/opt/homebrew/bin/brew

if ! command -v bazel &>/dev/null && ! command -v bazelisk &>/dev/null; then
  echo "→ installing bazelisk..."
  ${BREW} install bazelisk
fi
echo "→ bazel: $(bazel version 2>&1 | grep -E 'Bazelisk version|Build label' | head -1)"

for pkg in automake libtool cmake ninja; do
  if ! command -v "$pkg" &>/dev/null; then
    echo "→ installing $pkg..."
    ${BREW} install "$pkg"
  fi
done

if ! command -v java &>/dev/null; then
  echo "→ installing temurin jdk..."
  ${BREW} install --cask temurin
fi

# ── workspace ─────────────────────────────────────────────────────────────────
SLUG=$(echo "${ENVOY_REPO}" | tr '/' '_')
WORK_DIR="${HOME}/envoy-builder/${SLUG}"
SRC_DIR="${WORK_DIR}/src"
mkdir -p "${WORK_DIR}"

CLONE_URL="https://github.com/${ENVOY_REPO}.git"

if [[ -d "${SRC_DIR}/.git" ]]; then
  echo "→ updating existing clone..."
  cd "${SRC_DIR}"
  git remote set-url origin "${CLONE_URL}"
  git fetch --depth=1 origin "${COMMIT_SHA}" 2>&1 | tail -3
  # Reset tracked files before checkout so prior patches/bazelrc edits don't
  # contaminate this build. git clean handles untracked; git reset handles tracked.
  git reset --hard FETCH_HEAD
  git clean -fdx --exclude=.cache 2>/dev/null || true
else
  echo "→ cloning ${ENVOY_REPO} at ${COMMIT_SHA}..."
  git clone --depth=1 --no-checkout "${CLONE_URL}" "${SRC_DIR}"
  cd "${SRC_DIR}"
  git fetch --depth=1 origin "${COMMIT_SHA}" 2>&1 | tail -3
  git checkout FETCH_HEAD
fi

# Explicit cd ensures we're in SRC_DIR regardless of which branch above ran.
cd "${SRC_DIR}"
echo "→ at $(git rev-parse HEAD)"

# ── patch ─────────────────────────────────────────────────────────────────────
if [[ -n "${PATCH_URL:-}" ]]; then
  echo "→ fetching patch: ${PATCH_URL}"
  curl -fsSL "${PATCH_URL}" -o /tmp/incoming.patch
  echo "  $(wc -l < /tmp/incoming.patch) lines"
  git apply --stat /tmp/incoming.patch
  git apply /tmp/incoming.patch
  echo "→ patch applied"
fi

# ── BuildBuddy (remote cache only on macOS) ───────────────────────────────────
# Write the cache config OUTSIDE the workspace so the source tree stays clean
# and the binary reports "Clean" in its version string.
# A trap ensures the file is deleted on script exit so the key does not persist.
BAZELRC_CACHE="${WORK_DIR}/.bazelrc.cache"
rm -f "${BAZELRC_CACHE}"
trap 'rm -f "${BAZELRC_CACHE}"' EXIT
BAZEL_CACHE_ARGS=()
if [[ -n "${BUILDBUDDY_API_KEY:-}" ]]; then
  cat > "${BAZELRC_CACHE}" << EOF
build --remote_cache=grpcs://remote.buildbuddy.io
build --remote_header=x-buildbuddy-api-key=${BUILDBUDDY_API_KEY}
build --remote_upload_local_results
build --remote_timeout=3600
EOF
  BAZEL_CACHE_ARGS=("--bazelrc=${BAZELRC_CACHE}")
  echo "→ BuildBuddy remote cache enabled"
else
  echo "→ no BUILDBUDDY_API_KEY — local cache only"
fi

# ── build ─────────────────────────────────────────────────────────────────────
# --compilation_mode=opt:    optimized build (-O2, no debug assertions)
# --curses=no:               disable interactive Bazel UI (safe for remote/CI)
# --verbose_failures:        print the full compile command on error
# --linkopt=...:             SystemConfiguration framework required for macOS network APIs
# NOTE: to force all envoy_dynamic_module_callback_* symbols into the export trie on
#       macOS without a source patch, pass via --bazel-arg:
#         --bazel-arg="--linkopt=-Wl,-exported_symbol,_envoy_dynamic_module_callback_*"
#       The preferred fix is the visibility patch applied via --patch.
# --macos_minimum_os=11.0:   without this the LLVM toolchain defaults to 10.11, which
#                            lacks std::align_val_t (aligned allocation) needed by abseil
# --host_macos_minimum_os:   same constraint applied to host tools (e.g. protoc)
# Symbols are stripped post-build via system strip(1) (matching reference workflow),
# not via --strip=always which strips inside Bazel actions before linking completes.
echo "→ bazel build starting (--jobs=${BAZEL_JOBS})..."
bazel \
  ${BAZEL_CACHE_ARGS[@]+"${BAZEL_CACHE_ARGS[@]}"} \
  build \
  --compilation_mode=opt \
  --curses=no \
  --verbose_failures \
  "--linkopt=-Wl,-framework,SystemConfiguration" \
  --macos_minimum_os=11.0 \
  --host_macos_minimum_os=11.0 \
  --jobs="${BAZEL_JOBS}" \
  --show_progress_rate_limit=15 \
  ${BAZEL_EXTRA_ARGS[@]+"${BAZEL_EXTRA_ARGS[@]}"} \
  //source/exe:envoy-static

# ── locate and strip binary ────────────────────────────────────────────────────
ABS_BINARY="${SRC_DIR}/bazel-bin/source/exe/envoy-static"
if [[ ! -f "${ABS_BINARY}" ]]; then
  echo "ERROR: envoy-static not found at ${ABS_BINARY}" >&2
  exit 1
fi
chmod u+w "${ABS_BINARY}"
if [[ "${SKIP_STRIP:-}" != "1" ]]; then
  strip -x "${ABS_BINARY}"
fi
echo "→ binary: ${ABS_BINARY} ($(du -sh "${ABS_BINARY}" | cut -f1))"

# ── report dynamic_modules symbols (informational) ────────────────────────────
TOTAL=$(nm -g "${ABS_BINARY}" 2>/dev/null \
  | grep "envoy_dynamic_module_callback" | wc -l | tr -d ' ')
echo "→ dynamic_module_callback symbols: ${TOTAL}"

echo "BINARY_PATH:${ABS_BINARY}"
`

// remoteScriptLinux is the body of the bash script that runs inside the Linux
// OrbStack VM. It shares the same prologue variables as remoteScriptDarwin but
// uses apt-get for bootstrapping and omits macOS-specific Bazel flags.
const remoteScriptLinux = `
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

echo "→ host: $(hostname) $(uname -m) $(. /etc/os-release 2>/dev/null && echo "${PRETTY_NAME:-linux}")"

# ── bootstrap ─────────────────────────────────────────────────────────────────
if ! command -v bazel &>/dev/null && ! command -v bazelisk &>/dev/null; then
  echo "→ installing bazelisk..."
  ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
  sudo apt-get update -qq
  sudo apt-get install -y -qq curl ca-certificates
  curl -fsSL \
    "https://github.com/bazelbuild/bazelisk/releases/latest/download/bazelisk-linux-${ARCH}" \
    -o /usr/local/bin/bazelisk
  chmod +x /usr/local/bin/bazelisk
  ln -sf /usr/local/bin/bazelisk /usr/local/bin/bazel
fi
echo "→ bazel: $(bazel version 2>&1 | grep -E 'Bazelisk version|Build label' | head -1)"

PKGS=()
for check in "gcc:gcc" "cmake:cmake" "ninja:ninja-build" "libtoolize:libtool" \
             "automake:automake" "python3:python3" "java:default-jdk-headless" \
             "zip:zip" "unzip:unzip" "patch:patch"; do
  cmd="${check%%:*}"; pkg="${check##*:}"
  command -v "$cmd" &>/dev/null || PKGS+=("$pkg")
done
if [[ ${#PKGS[@]} -gt 0 ]]; then
  echo "→ installing ${PKGS[*]}..."
  sudo apt-get update -qq
  sudo apt-get install -y -qq "${PKGS[@]}"
fi

# ── workspace ─────────────────────────────────────────────────────────────────
SLUG=$(echo "${ENVOY_REPO}" | tr '/' '_')
WORK_DIR="${HOME}/envoy-builder/${SLUG}"
SRC_DIR="${WORK_DIR}/src"
mkdir -p "${WORK_DIR}"

CLONE_URL="https://github.com/${ENVOY_REPO}.git"

if [[ -d "${SRC_DIR}/.git" ]]; then
  echo "→ updating existing clone..."
  cd "${SRC_DIR}"
  git remote set-url origin "${CLONE_URL}"
  git fetch --depth=1 origin "${COMMIT_SHA}" 2>&1 | tail -3
  git reset --hard FETCH_HEAD
  git clean -fdx --exclude=.cache 2>/dev/null || true
else
  echo "→ cloning ${ENVOY_REPO} at ${COMMIT_SHA}..."
  git clone --depth=1 --no-checkout "${CLONE_URL}" "${SRC_DIR}"
  cd "${SRC_DIR}"
  git fetch --depth=1 origin "${COMMIT_SHA}" 2>&1 | tail -3
  git checkout FETCH_HEAD
fi

cd "${SRC_DIR}"
echo "→ at $(git rev-parse HEAD)"

# ── patch ─────────────────────────────────────────────────────────────────────
if [[ -n "${PATCH_URL:-}" ]]; then
  echo "→ fetching patch: ${PATCH_URL}"
  curl -fsSL "${PATCH_URL}" -o /tmp/incoming.patch
  echo "  $(wc -l < /tmp/incoming.patch) lines"
  git apply --stat /tmp/incoming.patch
  git apply /tmp/incoming.patch
  echo "→ patch applied"
fi

# ── BuildBuddy (remote cache) ─────────────────────────────────────────────────
BAZELRC_CACHE="${WORK_DIR}/.bazelrc.cache"
rm -f "${BAZELRC_CACHE}"
trap 'rm -f "${BAZELRC_CACHE}"' EXIT
BAZEL_CACHE_ARGS=()
if [[ -n "${BUILDBUDDY_API_KEY:-}" ]]; then
  cat > "${BAZELRC_CACHE}" << EOF
build --remote_cache=grpcs://remote.buildbuddy.io
build --remote_header=x-buildbuddy-api-key=${BUILDBUDDY_API_KEY}
build --remote_upload_local_results
build --remote_timeout=3600
EOF
  BAZEL_CACHE_ARGS=("--bazelrc=${BAZELRC_CACHE}")
  echo "→ BuildBuddy remote cache enabled"
else
  echo "→ no BUILDBUDDY_API_KEY — local cache only"
fi

# ── build ─────────────────────────────────────────────────────────────────────
echo "→ bazel build starting (--jobs=${BAZEL_JOBS})..."
bazel \
  ${BAZEL_CACHE_ARGS[@]+"${BAZEL_CACHE_ARGS[@]}"} \
  build \
  --compilation_mode=opt \
  --curses=no \
  --verbose_failures \
  --jobs="${BAZEL_JOBS}" \
  --show_progress_rate_limit=15 \
  ${BAZEL_EXTRA_ARGS[@]+"${BAZEL_EXTRA_ARGS[@]}"} \
  //source/exe:envoy-static

# ── locate and strip binary ────────────────────────────────────────────────────
ABS_BINARY="${SRC_DIR}/bazel-bin/source/exe/envoy-static"
if [[ ! -f "${ABS_BINARY}" ]]; then
  echo "ERROR: envoy-static not found at ${ABS_BINARY}" >&2
  exit 1
fi
chmod u+w "${ABS_BINARY}"
if [[ "${SKIP_STRIP:-}" != "1" ]]; then
  strip -x "${ABS_BINARY}"
fi
echo "→ binary: ${ABS_BINARY} ($(du -sh "${ABS_BINARY}" | cut -f1))"

# ── report dynamic_modules symbols (informational) ────────────────────────────
TOTAL=$(nm -g "${ABS_BINARY}" 2>/dev/null \
  | grep "envoy_dynamic_module_callback" | wc -l | tr -d ' ')
echo "→ dynamic_module_callback symbols: ${TOTAL}"

echo "BINARY_PATH:${ABS_BINARY}"
`

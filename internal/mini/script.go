package mini

// remoteScript returns the bash script that runs on the Mac mini.
// It is piped to `bash -s` over SSH stdin.
// Inputs arrive as env vars (set by the caller's `env KEY=val` prefix).
// Output: all build logs go to stdout/stderr; the single sentinel line
//
//	BINARY_PATH:/abs/path/to/envoy
//
// is emitted to stdout so the Go caller can extract it.
const remoteScript = `#!/usr/bin/env bash
set -euo pipefail
PATH="/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:$PATH"
export PATH

echo "→ host: $(hostname) $(uname -m) macOS $(sw_vers -productVersion)"

# ── bootstrap ─────────────────────────────────────────────────────────────────
# Ensure bazelisk, build deps, and Java are present. Safe to re-run.
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
  git checkout FETCH_HEAD
  git clean -fdx --exclude=.cache 2>/dev/null || true
else
  echo "→ cloning ${ENVOY_REPO} at ${COMMIT_SHA}..."
  git clone --depth=1 --no-checkout "${CLONE_URL}" "${SRC_DIR}"
  cd "${SRC_DIR}"
  git fetch --depth=1 origin "${COMMIT_SHA}" 2>&1 | tail -3
  git checkout FETCH_HEAD
fi

# Explicit cd after the if/else so subsequent sections are guaranteed to be
# in SRC_DIR regardless of which branch was taken.
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
rm -f .bazelrc.cache
if [[ -n "${BUILDBUDDY_API_KEY:-}" ]]; then
  cat >> .bazelrc.cache << EOF
build --remote_cache=grpcs://remote.buildbuddy.io
build --remote_header=x-buildbuddy-api-key=${BUILDBUDDY_API_KEY}
build --remote_upload_local_results
build --remote_timeout=3600
EOF
  echo "try-import %workspace%/.bazelrc.cache" >> .bazelrc
  echo "→ BuildBuddy remote cache enabled"
else
  echo "→ no BUILDBUDDY_API_KEY — local cache only"
fi

# ── build ─────────────────────────────────────────────────────────────────────
# -c opt:          optimized build (-O2, no debug assertions)
# --config=macos:  macOS PATH + tcmalloc=disabled + compiler flags
# --strip=always:  strip DWARF from output (~2x size reduction)
# --config=release does NOT exist in envoyproxy/envoy — do not use it.
# --//:contrib_enabled=false does NOT exist — do not use it.
echo "→ bazel build starting (--jobs=${BAZEL_JOBS})..."
bazel build \
  -c opt \
  --config=macos \
  --strip=always \
  --jobs="${BAZEL_JOBS}" \
  --show_progress_rate_limit=15 \
  //source/exe:envoy

# ── locate binary ─────────────────────────────────────────────────────────────
BINARY=$(bazel cquery -c opt --config=macos --output=files //source/exe:envoy 2>/dev/null | head -1 || true)
if [[ -z "${BINARY}" ]]; then
  BINARY=$(find bazel-bin/source/exe/ -maxdepth 1 -type f -executable 2>/dev/null | head -1 || true)
fi
if [[ -z "${BINARY}" || ! -f "${BINARY}" ]]; then
  echo "ERROR: could not find built binary in bazel-bin" >&2
  exit 1
fi

ABS_BINARY="${SRC_DIR}/${BINARY}"
echo "→ binary: ${ABS_BINARY} ($(du -sh "${ABS_BINARY}" | cut -f1))"

# ── verify dynamic_modules symbols ────────────────────────────────────────────
# A stock source build at envoyproxy/envoy main includes the http dynamic
# modules extension by default. If the nm check fails the extension was not
# compiled in — do not ship this binary.
echo "→ verifying dynamic_modules symbols..."
NM_HIT=$(nm -g "${ABS_BINARY}" 2>/dev/null \
  | grep "_envoy_dynamic_module_callback_http_filter_config_define_counter" \
  | wc -l | tr -d ' ')
if [[ "${NM_HIT}" -lt 1 ]]; then
  echo "ERROR: _envoy_dynamic_module_callback_http_filter_config_define_counter NOT found" >&2
  echo "ERROR: //source/extensions/filters/http/dynamic_modules was not compiled in" >&2
  echo "ERROR: check source/extensions/extensions_build_config.bzl at this commit" >&2
  exit 1
fi
TOTAL=$(nm -g "${ABS_BINARY}" 2>/dev/null \
  | grep "envoy_dynamic_module_callback_http_filter" | wc -l | tr -d ' ')
echo "→ dynamic_module http_filter symbols: ${TOTAL} (expect ≥50)"
if [[ "${TOTAL}" -lt 50 ]]; then
  echo "WARN: only ${TOTAL} http_filter callback symbols; expected ≥50" >&2
fi

echo "BINARY_PATH:${ABS_BINARY}"
`

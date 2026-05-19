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
echo "→ bazel: $(bazel version 2>&1 | grep -E 'Bazelisk version|Build label' | head -1)"

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
echo "BINARY_PATH:${ABS_BINARY}"
`

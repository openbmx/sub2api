#!/usr/bin/env bash
# =============================================================================
# Sub2API one-click deploy — builds THIS working tree and starts the stack.
# =============================================================================
# For a self-maintained fork: the published upstream image does not contain your
# changes, so this builds the image from source before starting.
#
#   ./one-click.sh              # named volumes (docker-compose.yml)
#   ./one-click.sh --local      # host directories (docker-compose.local.yml)
#   ./one-click.sh --no-build   # reuse the existing image / pull SUB2API_IMAGE
#
# Re-running is safe: secrets already present in .env are never regenerated.
# Rotating TOTP_ENCRYPTION_KEY would invalidate every stored TOTP secret and
# every saved Prompt Audit endpoint token, so it is written exactly once.
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR}"

BASE_COMPOSE="docker-compose.yml"
DO_BUILD=true
USE_LOCAL_DIRS=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --local)    BASE_COMPOSE="docker-compose.local.yml"; USE_LOCAL_DIRS=true ;;
    --no-build) DO_BUILD=false ;;
    -h|--help)  sed -n '2,16p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

# ---------------------------------------------------------------------------
# Prerequisites
# ---------------------------------------------------------------------------
if ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: docker is not installed or not on PATH." >&2
  exit 1
fi

if docker compose version >/dev/null 2>&1; then
  COMPOSE=(docker compose)
elif command -v docker-compose >/dev/null 2>&1; then
  COMPOSE=(docker-compose)
else
  echo "ERROR: neither 'docker compose' nor 'docker-compose' is available." >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Secret helpers
# ---------------------------------------------------------------------------
random_hex() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "${1:-32}"
  else
    # Fallback for images without openssl.
    head -c "${1:-32}" /dev/urandom | od -An -tx1 | tr -d ' \n'
  fi
}

# Values shipped in .env.example that must not survive into a real deployment.
is_placeholder() {
  case "$1" in
    ""|change_this_secure_password|your_password_here|changeme) return 0 ;;
    *) return 1 ;;
  esac
}

current_value() {
  # Last assignment wins, matching how docker compose reads the file.
  grep -E "^${1}=" .env 2>/dev/null | tail -n 1 | cut -d= -f2- || true
}

# set_if_blank KEY VALUE — only fills a missing/placeholder value, so an
# existing secret is preserved across re-runs.
set_if_blank() {
  local key="$1" value="$2" existing
  existing="$(current_value "${key}")"
  if ! is_placeholder "${existing}"; then
    return 0
  fi
  if grep -qE "^${key}=" .env; then
    # Portable in-place edit: BSD sed (macOS) requires an argument to -i.
    sed -i.bak -E "s|^${key}=.*|${key}=${value}|" .env && rm -f .env.bak
  else
    printf '%s=%s\n' "${key}" "${value}" >> .env
  fi
  GENERATED+=("${key}")
}

# ---------------------------------------------------------------------------
# .env
# ---------------------------------------------------------------------------
if [[ ! -f .env ]]; then
  cp .env.example .env
  echo "created .env from .env.example"
fi
chmod 600 .env 2>/dev/null || true

GENERATED=()
set_if_blank POSTGRES_PASSWORD "$(random_hex 16)"
set_if_blank JWT_SECRET "$(random_hex 32)"
# Must be 64 hex chars; the backend refuses to persist Prompt Audit endpoint
# tokens without a fixed key (they would not survive a restart).
set_if_blank TOTP_ENCRYPTION_KEY "$(random_hex 32)"
set_if_blank ADMIN_PASSWORD "$(random_hex 12)"

if ${DO_BUILD}; then
  # Point the stack at the image we are about to build.
  if is_placeholder "$(current_value SUB2API_IMAGE)"; then
    set_if_blank SUB2API_IMAGE "sub2api:local"
  fi
fi

if [[ ${#GENERATED[@]} -gt 0 ]]; then
  echo "filled in .env: ${GENERATED[*]}"
fi

if ${USE_LOCAL_DIRS}; then
  mkdir -p data postgres_data redis_data
fi

# ---------------------------------------------------------------------------
# Build + start
# ---------------------------------------------------------------------------
COMPOSE_FILES=(-f "${BASE_COMPOSE}")
UP_ARGS=(up -d)
if ${DO_BUILD}; then
  COMPOSE_FILES+=(-f docker-compose.build.yml)
  UP_ARGS+=(--build)
  if command -v git >/dev/null 2>&1 && git -C .. rev-parse --short HEAD >/dev/null 2>&1; then
    BUILD_COMMIT="$(git -C .. rev-parse --short HEAD)"
    export BUILD_COMMIT
  fi
  echo "building image from source (this takes a few minutes on a cold cache)..."
fi

"${COMPOSE[@]}" "${COMPOSE_FILES[@]}" "${UP_ARGS[@]}"

# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------
PORT="$(current_value SERVER_PORT)"; PORT="${PORT:-8080}"
echo
echo "Sub2API is starting on http://localhost:${PORT}"
echo "  admin email:    $(current_value ADMIN_EMAIL)"
echo "  admin password: $(current_value ADMIN_PASSWORD)"
echo
echo "Database migrations run automatically on first boot (AUTO_SETUP=true)."
echo "Follow startup with:"
echo "  ${COMPOSE[*]} ${COMPOSE_FILES[*]} logs -f sub2api"
echo
echo "Prompt Audit (风控中心) is OFF by default. To enable it:"
echo "  1. Admin > 系统设置 > turn on 风控总开关 (risk_control_enabled)"
echo "  2. Admin > 提示词审计 > add an audit node, response contract = 自定义提示词"
echo "  3. Use 试审 to check the prompt before enabling 同步阻止"

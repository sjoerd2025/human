#!/bin/sh
# Superset workspace setup — idempotent, safe to re-run.
set -eu
cd "$(dirname "$0")/.."

echo "[setup] node $(node --version), pnpm $(pnpm --version)"

# 1) Env: copy from the main checkout if the user keeps one there; else starter template
if [ -f .env ]; then
  echo "[setup] .env present — left untouched"
else
  main="$(git worktree list --porcelain | awk '/^worktree /{print $2; exit}')"
  if [ -n "$main" ] && [ -f "$main/.env" ]; then
    cp "$main/.env" .env && echo "[setup] copied .env from main checkout"
  else
    cat > .env <<'EOF'
# Starter env — edit in the MAIN checkout; new workspaces copy it from there.
PORT=5000
BASE_PATH=/
# DATABASE_URL=postgres://user:pass@localhost:5432/db   # required by api-server
EOF
    echo "[setup] wrote starter .env"
  fi
fi

# 2) Dependencies (shared pnpm store makes repeat installs fast)
pnpm install --frozen-lockfile
echo "[setup] done"

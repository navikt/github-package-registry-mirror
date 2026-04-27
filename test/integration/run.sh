#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

if [ -z "${GITHUB_TOKEN:-}" ]; then
  printf 'GITHUB_TOKEN not set. Enter a GitHub PAT with read:packages scope: ' >&2
  read -r GITHUB_TOKEN
  export GITHUB_TOKEN
fi

if [ -z "${GITHUB_TOKEN:-}" ]; then
  echo "ERROR: GITHUB_TOKEN is required" >&2
  exit 1
fi

echo "$GITHUB_TOKEN" >"$REPO_ROOT/github-token"

STORAGE_DIR="$(mktemp -d)"
SERVER_LOG="$STORAGE_DIR/server.log"
GRADLE_HOME_DIR="$(mktemp -d)"
export GRADLE_USER_HOME="$GRADLE_HOME_DIR"

cleanup() {
  [ -n "${SERVER_PID:-}" ] && kill "$SERVER_PID" 2>/dev/null || true
  rm -f "$REPO_ROOT/github-token"
  rm -rf "$STORAGE_DIR"
  rm -rf "$GRADLE_HOME_DIR"
}
trap cleanup EXIT

STORAGE_BACKEND=local STORAGE_PATH="$STORAGE_DIR" go run "$REPO_ROOT/." >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 20); do
  PORT=$(lsof -Pan -p "$SERVER_PID" -iTCP -sTCP:LISTEN 2>/dev/null | awk 'NR==2{print $9}' | sed 's/.*://')
  if [ -n "$PORT" ]; then
    break
  fi
  sleep 0.5
done

if [ -z "${PORT:-}" ]; then
  echo "ERROR: Server did not start in time" >&2
  echo "--- server log ---" >&2
  cat "$SERVER_LOG" >&2
  exit 1
fi

echo "Mirror running on port $PORT"

for _ in $(seq 1 10); do
  if curl -sf "http://localhost:$PORT/" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done

echo "Running Gradle dependency resolution..."
cd "$SCRIPT_DIR/gradle-app"
if ! gradle resolveDeps -PmirrorPort="$PORT" --no-daemon --stacktrace; then
  echo ""
  echo "--- server log ---" >&2
  cat "$SERVER_LOG" >&2
  exit 1
fi

echo "Integration test passed."

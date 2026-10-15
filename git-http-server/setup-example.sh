#!/usr/bin/env bash
set -euo pipefail

REPO_NAME="${1:-example}"
REPO_PATH="/srv/git/${REPO_NAME}.git"

docker compose exec -u www-data git-server \
  git init --bare --initial-branch=main "$REPO_PATH"

docker compose exec -u www-data git-server \
  git --git-dir="$REPO_PATH" config receive.unpackLimit 0

docker compose exec -u www-data git-server \
  git --git-dir="$REPO_PATH" config gc.auto 0

docker compose exec -u www-data git-server \
  git --git-dir="$REPO_PATH" config http.receivepack true

echo "Repository created: http://localhost:8080/git/${REPO_NAME}.git"
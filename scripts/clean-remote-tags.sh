#!/usr/bin/env bash
# Deletes old upstream tags from remote origin, keeping only v0.1.0 and future rogue tags.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
CORE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd -P)"
cd "${CORE_DIR}"

TAGS_TO_DELETE=(
    v1.10.0-core v1.11.0 v1.11.1 v1.11.10 v1.11.11 v1.11.12 v1.11.13 v1.11.14
    v1.11.14-rc-messnet-1 v1.11.14-rc-messnet-2 v1.11.15 v1.11.16 v1.11.17 v1.11.18
    v1.11.19 v1.11.2 v1.11.20 v1.11.21 v1.11.22 v1.11.3 v1.11.4 v1.11.5 v1.11.6
    v1.11.7 v1.11.8 v1.11.9 v1.12.0 v1.12.0-rc1 v1.12.0-rc2 v1.12.1 v1.12.10
    v1.12.11 v1.12.12 v1.12.13 v1.12.14 v1.12.15 v1.12.16 v1.12.17 v1.12.18
    v1.12.19 v1.12.2 v1.12.20 v1.12.21 v1.12.22 v1.12.23 v1.12.3 v1.12.4
    v1.12.5 v1.12.6 v1.12.7 v1.12.8 v1.12.9 v1.9.10-core v1.9.9-etclabscore
)

echo "Deleting ${#TAGS_TO_DELETE[@]} upstream tags from GitHub origin..."
git push origin --delete "${TAGS_TO_DELETE[@]}"
echo "Done! Only v0.1.0 remains."

#!/usr/bin/env bash
# Build the custom docker-logger image (with timestamped .err support) and
# push it to our Tencent Cloud container registry under two tags:
#   - :latest            (what scripts/demoway.cn-*/docker-compose.yml pulls)
#   - :YYYYMMDD-<sha>    (immutable version tag for rollback / auditing)
#
# Usage:
#   ./build_push.sh                # builds & pushes latest + dated tag
#   ./build_push.sh --no-push      # builds only, does not push
#   REGISTRY=<host>/<ns> ./build_push.sh   # override default registry
set -euo pipefail

cd "$(dirname "$0")"

REGISTRY="${REGISTRY:-ccr.ccs.tencentyun.com/node-js}"
IMAGE="${REGISTRY}/docker-logger"

SHA="$(git rev-parse --short HEAD 2>/dev/null || echo local)"
DATE="$(date +%Y%m%d)"
VERSION_TAG="${DATE}-${SHA}"

PUSH=1
for arg in "$@"; do
  case "$arg" in
    --no-push) PUSH=0 ;;
    *) echo "unknown arg: $arg" >&2; exit 1 ;;
  esac
done

echo "==> Building ${IMAGE}:latest and ${IMAGE}:${VERSION_TAG}"
docker build \
  --platform linux/amd64 \
  -t "${IMAGE}:latest" \
  -t "${IMAGE}:${VERSION_TAG}" \
  .

if [[ "${PUSH}" -eq 1 ]]; then
  echo "==> Pushing ${IMAGE}:latest"
  docker push "${IMAGE}:latest"
  echo "==> Pushing ${IMAGE}:${VERSION_TAG}"
  docker push "${IMAGE}:${VERSION_TAG}"
  echo
  echo "Done. On the target server run:"
  echo "  docker compose pull logger && docker compose up -d --no-deps --force-recreate logger"
else
  echo "==> Skipped push (--no-push)"
fi

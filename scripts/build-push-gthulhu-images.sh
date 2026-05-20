#!/usr/bin/env bash
set -euo pipefail

GTHULHU_DIR="${GTHULHU_DIR:-/home/ubuntu/Gthulhu}"
REGISTRY="${REGISTRY:-docker.io/d11nn}"
PUSH="${PUSH:-true}"
PLATFORM="${PLATFORM:-linux/amd64}"

cd "$GTHULHU_DIR"

short_sha="$(git rev-parse --short HEAD)"
tag="${TAG:-woms-integration-${short_sha}}"
base_image="${REGISTRY}/gthulhu:${tag}"
scx_image="${REGISTRY}/gthulhu-scx:${tag}"
api_image="${REGISTRY}/gthulhu-api:${tag}"

echo "Building Gthulhu images from $(git rev-parse --show-toplevel) at ${short_sha}"
echo "Target images:"
echo "  ${scx_image}"
echo "  ${api_image}"

docker build --platform "$PLATFORM" -t "$base_image" -f Dockerfile .
docker build --platform "$PLATFORM" --build-arg "BASE_IMAGE=${base_image}" -t "$scx_image" -f Dockerfile.scx .
docker build --platform "$PLATFORM" -t "$api_image" -f api/Dockerfile api

if [ "$PUSH" = "true" ]; then
  docker push "$scx_image"
  docker push "$api_image"
fi

cat <<EOF
GTHULHU_SHORT_SHA=${short_sha}
GTHULHU_SCX_IMAGE=${scx_image}
GTHULHU_API_IMAGE=${api_image}
Helm overrides:
  --set gthulhu.scheduler.image.repository=${REGISTRY}/gthulhu-scx
  --set gthulhu.scheduler.image.tag=${tag}
  --set gthulhu.scheduler.sidecar.image.repository=${REGISTRY}/gthulhu-api
  --set gthulhu.scheduler.sidecar.image.tag=${tag}
  --set gthulhu.manager.image.repository=${REGISTRY}/gthulhu-api
  --set gthulhu.manager.image.tag=${tag}
EOF

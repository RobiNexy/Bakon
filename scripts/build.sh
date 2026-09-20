#!/usr/bin/env bash
# 交叉编译全部发布平台，产物为直接二进制（不打包），输出到 dist/。
# 用法：
#   scripts/build.sh                 # 版本号取 git describe
#   VERSION=v0.1.0 scripts/build.sh  # 显式指定版本号
# 约束：纯 Go（CGO_ENABLED=0），产物为静态二进制；
#       Termux 使用 linux/arm64 / linux/arm 构建即可运行。
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}
DATE=${DATE:-$(date -u '+%Y-%m-%dT%H:%M:%SZ')}
DIST=${DIST:-dist}

MODULE=$(go list -m)
LDFLAGS="-s -w -X ${MODULE}/internal/cli.version=${VERSION} -X ${MODULE}/internal/cli.commit=${COMMIT} -X ${MODULE}/internal/cli.date=${DATE}"

# 平台列表：os/arch[:goarm]
PLATFORMS=(
	"linux/amd64"
	"linux/arm64"
	"linux/arm:7"
	"darwin/amd64"
	"darwin/arm64"
	"windows/amd64"
	"windows/arm64"
)

rm -rf "$DIST"
mkdir -p "$DIST"

for platform in "${PLATFORMS[@]}"; do
	os=${platform%%/*}
	rest=${platform#*/}
	arch=${rest%%:*}
	goarm=""
	[[ $rest == *:* ]] && goarm=${rest#*:}
	suffix=${arch}${goarm:+v${goarm}}

	export GOOS=$os GOARCH=$arch CGO_ENABLED=0
	if [ -n "$goarm" ]; then
		export GOARM=$goarm
	else
		unset GOARM 2>/dev/null || true
	fi

	ext=""
	[ "$os" = "windows" ] && ext=".exe"
	name="bakon_${VERSION}_${os}-${suffix}${ext}"
	go build -trimpath -ldflags "$LDFLAGS" -o "$DIST/$name" .
	echo "built $name"
done

(cd "$DIST" && sha256sum ./bakon_* > checksums.txt)
echo "artifacts in $DIST/"

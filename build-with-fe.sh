#!/usr/bin/env bash
#
# build-with-fe.sh — 一键将自建 n9e/fe 前端（含 OpenObserve）嵌入正式二进制
#
# 用法：
#   ./build-with-fe.sh                 # 版本号自动读取 ../nightingale-fe/package.json
#   ./build-with-fe.sh v9.0.0          # 也可在命令行显式指定版本号
#   VERSION=v9.0.0 ./build-with-fe.sh  # 或用环境变量
#   FE_ROOT=/path/to/fe ./build-with-fe.sh
#
# ⚠️ 本脚本刻意不调用 fe.sh —— fe.sh 会重新 git clone 官方 n9e/fe 并覆盖 pub/，
#    会把我们的 OpenObserve 改动冲掉。这里走手动嵌入流程，保留自建前端。
#
set -euo pipefail

N9E_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FE_ROOT="${FE_ROOT:-$N9E_ROOT/../nightingale-fe}"
PUB_DIR="$N9E_ROOT/pub"
FRONT_DIR="$N9E_ROOT/front"

# 版本号优先级：命令行参数 > 环境变量 VERSION > fe package.json
if [ $# -ge 1 ]; then
  VERSION="$1"
elif [ -n "${VERSION:-}" ]; then
  : # 使用环境变量 VERSION
else
  # ⚠️ node 是 Windows 二进制，unix 风格路径 /d/ai/... 会被误解成 C:\d\...
  #    用 cygpath -m 转成 D:/ai/...（Windows 盘符 + 正斜杠）Node 才能正确读取
  PKG_JSON="$(cygpath -m "$FE_ROOT/package.json" 2>/dev/null || echo "$FE_ROOT/package.json")"
  VERSION="$(node -e "console.log(require('$PKG_JSON').version)" 2>/dev/null || echo unknown)"
fi

echo "==> FE repo  : $FE_ROOT"
echo "==> N9e root : $N9E_ROOT"
echo "==> Version  : $VERSION"

# 1) 构建前端
echo "==> [1/4] 构建前端 (npm run build) ..."
( cd "$FE_ROOT" && npm run build )

# 2) 复制前端产物到后端 pub/（先清空再复制，避免残留旧资源被 statik 一起嵌入）
echo "==> [2/4] 复制 pub/ -> $PUB_DIR ..."
rm -rf "$PUB_DIR"
mkdir -p "$PUB_DIR"
cp -r "$FE_ROOT/pub/." "$PUB_DIR/"

# 3) statik 嵌入
# ⚠️ statik 是 Windows 原生二进制，绝对 unix 路径（如 /d/ai/...）会被它误解成 C:\d\...
#    因此必须 cd 到 N9E_ROOT 后用【相对路径】 pub / front 调用。
echo "==> [3/4] statik 嵌入前端 ..."
cd "$N9E_ROOT"
STATIK_BIN="$(go env GOPATH)/bin/statik"
if [ ! -x "$STATIK_BIN" ]; then
  echo "    statik 未安装，正在 go install github.com/rakyll/statik@latest ..."
  go install github.com/rakyll/statik@latest
fi
"$STATIK_BIN" -src=pub -dest=front -f

# 4) 构建二进制（注入版本号，否则关于页 / 顶部版本角标显示 unknown）
echo "==> [4/4] 构建二进制 (go build -ldflags) ..."
cd "$N9E_ROOT"
go build -ldflags "-X github.com/ccfos/nightingale/v6/pkg/version.Version=$VERSION" -o n9e ./cmd/center/main.go

echo "==> 构建完成。验证版本号："
./n9e -version
echo "==> 提示：二进制已生成于 $N9E_ROOT/n9e。重启服务进程即可生效（本脚本不自动重启）。"

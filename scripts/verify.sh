#!/usr/bin/env bash
# scripts/verify.sh — 本地与 CI 共用的验证入口。
#
# 阶段顺序：build → vet → smoke(record/seal/compare/browse)；race 为可选高成本阶段。
# 任一阶段失败即停止：非零退出码，日志标明停在哪个阶段。
# 脚本被中断（Ctrl-C / SIGTERM）时把 SIGINT 转给当前阶段的子进程，smoke.py
# 会停掉它启动的 stratad 服务进程；临时编译产物与数据目录位于系统临时目录并随之清理。
#
# 测试模式为 deferred：本入口刻意不运行 go test，空测试通过不代表覆盖。
set -euo pipefail
cd "$(dirname "$0")/.."

child=''
failed=''

forward() { # 把中断转给当前阶段的子进程，让 smoke.py 清理服务进程
  if [ -n "$child" ]; then
    kill -INT "$child" 2>/dev/null || true
  fi
}
trap forward INT TERM

on_exit() {
  rc=$?
  trap - EXIT INT TERM
  forward
  if [ -n "$failed" ]; then
    echo "verify: FAILED at stage [$failed]" >&2
  elif [ "$rc" -eq 0 ]; then
    echo "verify: OK"
  fi
  exit "$rc"
}
trap on_exit EXIT

usage() {
  cat <<'EOF'
usage: scripts/verify.sh [stage]

  all     build → vet → smoke（默认；STRATA_VERIFY_RACE=1 时追加 race）
  build   go build ./...
  vet     go vet ./...
  smoke   四组 HTTP 冒烟：record / seal / compare / browse
  race    可选高成本竞态检查，需 CGO_ENABLED=1 与 C 工具链（见 README）

退出码：0 通过；非 0 为失败阶段的退出码；2 用法错误；127 缺少前置工具。
EOF
}

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "verify: missing prerequisite '$1' on PATH" >&2
    exit 127
  fi
}

run() {
  stage="$1"; shift
  echo
  echo "==> [$stage] $*"
  # 后台启动以便 wait 可被信号中断并转发；后台作业默认继承被忽略的
  # SIGINT，子壳先恢复默认处理，保证 python 收到转发的 SIGINT 后能清理。
  ( trap - INT; exec "$@" ) &
  child=$!
  rc=0
  wait "$child" || rc=$?
  child=''
  if [ "$rc" -ne 0 ]; then
    failed="$stage"
    echo "==> [$stage] failed (exit $rc)" >&2
    return "$rc"
  fi
  echo "==> [$stage] ok"
}

stage_build() {
  need go
  run build go build ./...
}

stage_vet() {
  need go
  run vet go vet ./...
}

stage_smoke() {
  need go
  need python3
  for wf in record seal compare browse; do
    run "smoke:$wf" python3 scripts/smoke.py "$wf"
  done
}

stage_race() {
  need go
  need python3
  if [ "$(go env CGO_ENABLED)" != "1" ]; then
    echo "verify: race requires CGO_ENABLED=1 and a C toolchain (Linux: gcc, macOS: Xcode CLT)" >&2
    exit 1
  fi
  for wf in record seal compare browse; do
    run "race:$wf" env STRATA_SMOKE_RACE=1 python3 scripts/smoke.py "$wf"
  done
}

case "${1:-all}" in
  all)
    stage_build
    stage_vet
    stage_smoke
    if [ "${STRATA_VERIFY_RACE:-0}" = "1" ]; then
      stage_race
    fi
    ;;
  build) stage_build ;;
  vet) stage_vet ;;
  smoke) stage_smoke ;;
  race) stage_race ;;
  -h|--help|help) usage ;;
  *)
    echo "verify: unknown stage '$1'" >&2
    usage >&2
    exit 2
    ;;
esac

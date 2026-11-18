#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/../.."

logs="${WORKBENCH_DATA:-$HOME/.yovoice}/logs"
mkdir -p "$logs"
# 提前创建日志文件；跟随文件名以兼容服务启动和引擎重启时的截断。
# [fork-local] 追加 service.log（下载通道判定 download.transport 等诊断日志）与 sdk.log
# （venv 里 modelscope / huggingface SDK 的原始下载进度），让调试时在终端直接看到，省得另开 tail。
files=("$logs/host.log" "$logs/host-service.log" "$logs/engine.log" "$logs/engine.log.out" "$logs/service.log" "$logs/sdk.log")
touch "${files[@]}"
tail -n 0 -F "${files[@]}" &
log_pid=$!
trap 'kill "$log_pid" 2>/dev/null || true; wait "$log_pid" 2>/dev/null || true' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# 经 LaunchServices 启动，让麦克风授权归属于 App，而不是终端或 IDE。
args=(-W --stdout "$logs/host.log" --stderr "$logs/host.log")
for key in WORKBENCH_DATA WORKBENCH_DEBUG WORKBENCH_SMOKE_SCRIPT YOVOICE_COMMUNITY_API YOVOICE_PYTHON; do
    if [[ -n "${!key:-}" ]]; then args+=(--env "$key=${!key}"); fi
done
open "${args[@]}" "$PWD/artifacts/macos-arm64/yovoice.app"

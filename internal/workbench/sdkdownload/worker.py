"""用官方 SDK（modelscope / huggingface_hub）下载单个模型文件，并把进度以单行 JSON 打到 stdout。

由 yovoice-service 以子进程方式调用（见 sdk.go）。约定：
- 进度行：  {"done": <已写字节>, "total": <总字节>}
- 完成行：  {"event": "done"}
- 出错时把回溯写到 stderr 并以非零码退出；调用方据此回退或报错。

进度采用“轮询目标目录里正在增长的字节数”这一 SDK 无关的兜底信号：
两个官方 SDK 都没有稳定的进程内进度回调，直接读它们的临时文件大小最可靠。
"""

import argparse
import json
import os
import sys
import threading
from pathlib import Path


def emit(payload):
    """向 stdout 打印一行机器可读 JSON 并立即刷新。"""
    sys.stdout.write(json.dumps(payload) + "\n")
    sys.stdout.flush()


def human(n):
    """把字节数格式化成人类可读大小，供 stderr 进度行使用。"""
    m = float(n)
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if m < 1024 or unit == "TB":
            return "%.1f%s" % (m, unit)
        m /= 1024
    return "%.1fTB" % m


def log(msg):
    """向 stderr 写一行原始日志并刷新（供服务转写到 logs/sdk.log）。"""
    sys.stderr.write(msg + "\n")
    sys.stderr.flush()


def watch_size(root: Path):
    """返回 root 目录树内所有普通文件的最大字节数，用于估计单个下载的增长量。"""
    largest = 0
    try:
        for dirpath, _dirs, files in os.walk(root):
            for name in files:
                try:
                    size = os.path.getsize(os.path.join(dirpath, name))
                except OSError:
                    continue
                if size > largest:
                    largest = size
    except OSError:
        pass
    return largest


def poll_progress(root: Path, total: int, stop: threading.Event):
    """后台轮询正在增长的临时文件大小，节流地打印进度直到下载结束。

    stdout 供服务解析（机器可读 JSON）；stderr 同步写一行人类可读进度，
    与 SDK 自带的 tqdm 输出一起落到 logs/sdk.log，便于实时排查。
    """
    last_done = -1
    while not stop.is_set():
        done = watch_size(root)
        if total > 0:
            done = min(done, total)
        if done != last_done:
            emit({"done": done, "total": total})
            if total > 0:
                log("progress %5.1f%%  %s / %s" % (done * 100.0 / total, human(done), human(total)))
            else:
                log("progress %s" % human(done))
            last_done = done
        stop.wait(0.5)


def download_huggingface(args, dest_dir: Path):
    import huggingface_hub  # noqa: F401  仅确认可导入
    from huggingface_hub import hf_hub_download

    kwargs = {
        "repo_id": args.repo,
        "filename": args.filename,
        "revision": args.revision,
        "local_dir": str(dest_dir),
    }
    try:
        return hf_hub_download(**kwargs)
    except TypeError:
        # 旧版本没有 local_dir 参数时退回缓存目录，再复制出去。
        return hf_hub_download(
            repo_id=args.repo, filename=args.filename, revision=args.revision
        )


def download_modelscope(args, dest_dir: Path):
    from modelscope import model_file_download

    kwargs = {
        "model_id": args.repo,
        "file_path": args.filename,
        "revision": args.revision,
    }
    try:
        return model_file_download(local_dir=str(dest_dir), **kwargs)
    except TypeError:
        return model_file_download(**kwargs)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", required=True, choices=["modelscope", "huggingface"])
    parser.add_argument("--repo", required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--filename", required=True)
    parser.add_argument("--dest", required=True)
    parser.add_argument("--total", type=int, default=0)
    parser.add_argument("--endpoint", default="")
    args = parser.parse_args()

    dest = Path(args.dest)
    dest.parent.mkdir(parents=True, exist_ok=True)

    # Windows 直连 Hugging Face 时 xet 会把字节写进独立缓存，轮询看不到增长（UI 卡 0%），
    # 因此强制经典 HTTP。endpoint 也须在导入 huggingface_hub 前经环境变量设定。
    os.environ["HF_HUB_DISABLE_XET"] = "1"
    if args.source == "huggingface" and args.endpoint:
        os.environ["HF_ENDPOINT"] = args.endpoint

    # 下载落到独立临时目录，只包含本文件的临时增长，轮询不会误读别的文件。
    # [fork-local] 关键：暂停后续传必须保留这个临时目录——SDK 靠它里面的 *.incomplete 分片断点续传。
    # 之前每次启动都 rmtree 清空，导致点“继续”又从 0 重下、进度条塌回 0；这里改为只确保目录存在、不清空。
    # 换版本 / 丢弃任务由上层用新的传输目录（不同 Token → 不同 dest）天然隔离，不会串档。
    work = Path(str(dest) + ".sdktmp")
    work.mkdir(parents=True, exist_ok=True)

    stop = threading.Event()
    watcher = threading.Thread(target=poll_progress, args=(work, args.total, stop), daemon=True)
    watcher.start()
    try:
        if args.source == "modelscope":
            result = download_modelscope(args, work)
        else:
            result = download_huggingface(args, work)
    finally:
        stop.set()
        watcher.join(timeout=2)

    src = Path(result)
    if not src.is_file():
        # 少数版本返回值不是最终路径：在临时目录里找与目标同名的文件。
        matches = [p for p in work.rglob(Path(args.filename).name) if p.is_file()]
        if not matches:
            raise FileNotFoundError("SDK 未产出目标文件: " + str(result))
        src = max(matches, key=lambda p: p.stat().st_size)

    if args.total > 0:
        size = src.stat().st_size
        if size != args.total:
            raise ValueError("下载大小与预期不符: got=%d want=%d" % (size, args.total))

    os.replace(src, dest)
    import shutil

    shutil.rmtree(work, ignore_errors=True)
    log("done  %s" % human(dest.stat().st_size))
    emit({"done": dest.stat().st_size, "total": max(args.total, dest.stat().st_size)})
    emit({"event": "done"})


if __name__ == "__main__":
    try:
        main()
    except Exception:  # noqa: BLE001  顶层兜底：把回溯给调用方，非零退出触发回退/报错
        import traceback

        traceback.print_exc(file=sys.stderr)
        sys.exit(1)

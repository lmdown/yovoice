#!/usr/bin/env python3
"""[fork-local] 刷新 catalog.fork.json 里自量化档的 sha256 与 size。

用途：重新量化并上传到 ModelScope（rookie6667/* 仓库）后，文件名不变但内容变了，
钉死在附加清单里的 sha256 会对不上，导致下载校验失败（download.go 末尾 `if h != hash`）。
本脚本回到 ModelScope 仓库元数据重取每个自量化文件的 sha256 与字节大小并就地写回，
不扫描本地 gguf（有的档可能根本没下载）。

取值方式（纯远程）：
  * sha256：ModelScope 的 `resolve/master/<path>` 会 302 跳到按内容寻址的
    `lfs-objects/xx/yy/REST`，把三段拼起来（xx+yy+REST）就是该文件的 sha256。
  * size：对同一地址发 `Range: bytes=0-0` 并跟随跳转，末跳返回 `Content-Range: */总计`。

用法：
    python3 scripts/refresh-fork-catalog.py            # 取新值并写回
    python3 scripts/refresh-fork-catalog.py --dry-run  # 只报告差异，不改动
    python3 scripts/refresh-fork-catalog.py --id index-2.5-q3   # 只刷新指定档
"""

import argparse
import json
import re
import sys
import urllib.error
import urllib.request
from pathlib import Path

# rookie6667 上是我们自上传的仓库；audio-cpp 的 sidecars/vae 是上游产物，重传不动它们。
SELF_REPO_PREFIX = "rookie6667"

LFS_OID = re.compile(r"lfs-objects/([0-9a-f]{2})/([0-9a-f]{2})/([0-9a-f]+)")
CONTENT_RANGE_TOTAL = re.compile(r"bytes \d+-\d+/(\d+)")
UA = {"User-Agent": "yovoice-fork-catalog-refresh"}


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """拦住自动跳转，好从 302 的 Location 里读出内容哈希。"""

    def redirect_request(self, req, fp, code, msg, headers, newurl):  # noqa: D102
        return None


def _resolve_url(repo: str, path: str) -> str:
    return f"https://www.modelscope.cn/models/{repo}/resolve/master/{path}"


def fetch_sha256(url: str) -> str:
    """跟到第一跳 Location，解析 lfs-objects 路径拼出 sha256。"""
    opener = urllib.request.build_opener(_NoRedirect)
    req = urllib.request.Request(url, headers=UA)
    try:
        opener.open(req, timeout=30)
        raise ValueError("未收到内容寻址跳转，可能不是 LFS 文件")
    except urllib.error.HTTPError as e:
        if e.code not in (301, 302, 303, 307, 308):
            raise
        location = e.headers.get("Location", "")
    match = LFS_OID.search(location)
    if not match:
        raise ValueError(f"跳转地址里没有 lfs-objects 哈希段：{location}")
    a, b, rest = match.groups()
    oid = a + b + rest
    if len(oid) != 64:
        raise ValueError(f"解析出的 sha256 长度不是 64：{oid}")
    return oid


def fetch_size(url: str) -> int:
    """发 Range 请求并跟随跳转，从末跳 Content-Range 读文件总字节数。"""
    req = urllib.request.Request(url, headers={**UA, "Range": "bytes=0-0"})
    with urllib.request.urlopen(req, timeout=60) as resp:
        cr = resp.headers.get("Content-Range", "")
        m = CONTENT_RANGE_TOTAL.search(cr)
        if m:
            return int(m.group(1))
        cl = resp.headers.get("Content-Length", "")
        if cl.isdigit():
            return int(cl)
    raise ValueError("响应缺少 Content-Range/Content-Length，取不到大小")


def refresh_file(model: dict, entry: dict) -> bool:
    """按 entry 的有效来源仓库刷新其 sha256/size；非自量化仓库返回 False 不动。"""
    repo = entry.get("repo") or model.get("modelScope") or model.get("repo") or ""
    if not repo.startswith(SELF_REPO_PREFIX):
        return False
    url = _resolve_url(repo, entry["path"])
    entry["sha256"], entry["size"] = fetch_sha256(url), fetch_size(url)
    return True


def refresh_model(model: dict) -> list[str]:
    """刷新一个档，返回改动说明行。"""
    changes: list[str] = []
    files = model.get("files")
    if files:
        # 多文件包：只重取来源为自量化仓库的文件（主 gguf），sidecars/vae 保持上游值。
        for entry in files:
            before = (entry.get("sha256"), entry.get("size"))
            if refresh_file(model, entry) and before != (entry["sha256"], entry["size"]):
                changes.append(f"{model['id']} · {entry['path']}: "
                               f"{before[0][:8]}…/{before[1]} → {entry['sha256'][:8]}…/{entry['size']}")
        # 顶层 sha256 取主文件、size 取全部文件之和（与既有 yue2 条目口径一致）。
        main = next((f for f in files if f["path"] == model.get("remotePath")), None)
        if main:
            model["sha256"] = main["sha256"]
        model["size"] = sum(f["size"] for f in files)
    else:
        # 单文件档：顶层即主文件。
        before = (model.get("sha256"), model.get("size"))
        entry = {"path": model["remotePath"]}
        if refresh_file(model, entry):
            model["sha256"], model["size"] = entry["sha256"], entry["size"]
            if before != (model["sha256"], model["size"]):
                changes.append(f"{model['id']}: {before[0][:8]}…/{before[1]} → "
                               f"{model['sha256'][:8]}…/{model['size']}")
    return changes


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description="刷新附加模型清单里自量化档的 sha256/size")
    parser.add_argument("--catalog", type=Path,
                        default=Path(__file__).resolve().parents[1] / "internal/catalog/catalog.fork.json",
                        help="catalog.fork.json 路径（默认仓库内那份）")
    parser.add_argument("--id", action="append", dest="ids", metavar="ID",
                        help="只刷新指定 id（可重复）；默认刷新全部自量化档")
    parser.add_argument("--dry-run", action="store_true", help="只报告差异，不写回")
    args = parser.parse_args(argv)

    if not args.catalog.is_file():
        print(f"找不到清单：{args.catalog}", file=sys.stderr)
        return 2
    doc = json.loads(args.catalog.read_text(encoding="utf-8"))

    all_changes: list[str] = []
    errors: list[str] = []
    for model in doc.get("models", []):
        if args.ids and model.get("id") not in args.ids:
            continue
        try:
            all_changes += refresh_model(model)
        except (urllib.error.URLError, ValueError, OSError) as e:
            errors.append(f"{model.get('id')}: 取值失败 {e}")

    for line in all_changes:
        print("更新", line)
    for line in errors:
        print("错误", line, file=sys.stderr)

    if errors:
        print("有文件取值失败，未写回清单。", file=sys.stderr)
        return 1
    if not all_changes:
        print("sha256/size 均已与 ModelScope 一致，无需改动。")
        return 0
    if args.dry_run:
        print("（--dry-run）以上为将要写回的差异，未改动文件。")
        return 0

    args.catalog.write_text(json.dumps(doc, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"已写回 {args.catalog.name}（{len(all_changes)} 处）。记得再跑 go test -race ./internal/catalog/...")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

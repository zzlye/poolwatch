#!/usr/bin/env python3
"""生成安卓安装包的在线更新元数据。"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from datetime import datetime, timezone
from pathlib import Path


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="生成安卓在线更新清单")
    parser.add_argument("--apk-path", type=Path, help="安装包路径")
    parser.add_argument(
        "--gradle-config",
        required=True,
        type=Path,
        help="安卓应用的 Gradle 配置路径",
    )
    parser.add_argument("--output", type=Path, help="元数据输出路径")
    parser.add_argument("--tag", required=True, help="发布标签")
    parser.add_argument("--repository", help="GitHub 仓库，例如 zzlye/poolwatch")
    parser.add_argument(
        "--check-version-only",
        action="store_true",
        help="只检查标签与安卓版本是否一致",
    )
    parser.add_argument(
        "--mandatory",
        action="store_true",
        help="将本次更新标记为必须更新",
    )
    return parser.parse_args()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_release_info() -> dict[str, object]:
    """从标准输入读取 GitHub Release 返回值，避免在命令行展开多行更新说明。"""
    try:
        value = json.load(sys.stdin)
    except json.JSONDecodeError as exc:
        raise SystemExit(f"GitHub Release 信息不是有效 JSON：{exc}") from exc
    if not isinstance(value, dict):
        raise SystemExit("GitHub Release 信息必须是 JSON 对象")
    return value


def read_android_version(path: Path) -> tuple[int, str]:
    """读取发布变体使用的 versionCode 与 versionName。"""
    if not path.is_file():
        raise SystemExit(f"未找到 Gradle 配置：{path}")
    source = path.read_text(encoding="utf-8")
    code_match = re.search(r"^\s*versionCode\s*=\s*(\d+)\s*$", source, re.MULTILINE)
    name_match = re.search(r'^\s*versionName\s*=\s*"([^"]+)"\s*$', source, re.MULTILINE)
    if code_match is None or name_match is None:
        raise SystemExit("未能从 Gradle 配置读取 versionCode 或 versionName")
    version_code = int(code_match.group(1))
    if version_code < 1:
        raise SystemExit("versionCode 必须大于零")
    return version_code, name_match.group(1)


def find_release_asset(release: dict[str, object], name: str) -> dict[str, object]:
    """按固定资产名确认 Release 已经公开对应文件。"""
    assets = release.get("assets")
    if not isinstance(assets, list):
        raise SystemExit("GitHub Release 信息缺少 assets 数组")
    for asset in assets:
        if isinstance(asset, dict) and asset.get("name") == name:
            return asset
    raise SystemExit(f"GitHub Release 中未找到资产：{name}")


def main() -> int:
    args = parse_args()
    if not re.fullmatch(r"android-v[^/]+", args.tag):
        raise SystemExit(f"发布标签必须使用 android-v 前缀：{args.tag}")
    version_code, version_name = read_android_version(args.gradle_config)
    tag_version = args.tag.removeprefix("android-v")
    if tag_version != version_name:
        raise SystemExit(
            f"标签版本与安装包版本不一致：标签={tag_version}，versionName={version_name}"
        )
    if args.check_version_only:
        return 0
    if args.apk_path is None or args.output is None or not args.repository:
        raise SystemExit("生成更新清单时必须提供 --apk-path、--output 与 --repository")
    if not args.apk_path.is_file():
        raise SystemExit(f"未找到安装包：{args.apk_path}")

    release = read_release_info()
    release_url = str(release.get("url") or f"https://github.com/{args.repository}/releases/tag/{args.tag}")
    published_at = str(release.get("publishedAt") or datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"))
    release_notes = release.get("body")
    if not isinstance(release_notes, str):
        release_notes = ""
    # 同时限制字符数和 UTF-8 字节数，确保服务端与手机端都能稳定解析更新说明。
    release_notes = release_notes[:10_000]
    release_notes = release_notes.encode("utf-8")[: 30 * 1024].decode(
        "utf-8", errors="ignore"
    )

    apk_name = f"poolwatch-{args.tag}.apk"
    if args.apk_path.name != apk_name:
        raise SystemExit(f"安装包名称不符合发布约定：应为 {apk_name}")
    apk_asset = find_release_asset(release, apk_name)
    find_release_asset(release, f"{apk_name}.sha256")
    download_url = apk_asset.get("url")
    if not isinstance(download_url, str) or not download_url.startswith("https://github.com/"):
        raise SystemExit("GitHub Release 安装包缺少公开下载地址")
    size_bytes = args.apk_path.stat().st_size
    asset_size = apk_asset.get("size")
    if not isinstance(asset_size, int) or asset_size != size_bytes:
        raise SystemExit(
            f"本地安装包与 Release 资产大小不一致：本地={size_bytes}，Release={asset_size}"
        )
    metadata = {
        "versionCode": version_code,
        "versionName": version_name,
        "tag": args.tag,
        "downloadUrl": download_url,
        "sha256": sha256_file(args.apk_path),
        "sizeBytes": size_bytes,
        "mandatory": bool(args.mandatory),
        "releaseUrl": release_url,
        "releaseNotes": release_notes,
        "publishedAt": published_at,
    }

    args.output.parent.mkdir(parents=True, exist_ok=True)
    # 使用固定排序和缩进，便于审查发布资产并保持重复构建结果稳定。
    args.output.write_text(
        json.dumps(metadata, ensure_ascii=False, indent=2, sort_keys=False) + "\n",
        encoding="utf-8",
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

#!/usr/bin/env python3
"""验证安卓在线更新清单生成脚本。"""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT_PATH = Path(__file__).with_name("generate-android-update-manifest.py")


class GenerateAndroidUpdateManifestTest(unittest.TestCase):
    def test_draft_release_urls_are_replaced_with_stable_tag_urls(self) -> None:
        """草稿发布的临时地址不得写入最终更新清单。"""
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            gradle_config = root / "build.gradle.kts"
            gradle_config.write_text(
                'versionCode = 5\nversionName = "1.1.3"\n', encoding="utf-8"
            )
            apk_name = "poolwatch-android-v1.1.3.apk"
            apk_path = root / apk_name
            apk_path.write_bytes(b"signed-apk-fixture")
            output_path = root / "android-update.json"
            release = {
                "assets": [
                    {
                        "name": apk_name,
                        "size": apk_path.stat().st_size,
                        "url": (
                            "https://github.com/zzlye/poolwatch/releases/download/"
                            f"untagged-temporary/{apk_name}"
                        ),
                    },
                    {
                        "name": f"{apk_name}.sha256",
                        "size": 95,
                        "url": (
                            "https://github.com/zzlye/poolwatch/releases/download/"
                            f"untagged-temporary/{apk_name}.sha256"
                        ),
                    },
                ],
                "body": "更新说明",
                "publishedAt": "2026-07-21T17:58:00Z",
                "url": (
                    "https://github.com/zzlye/poolwatch/releases/tag/"
                    "untagged-temporary"
                ),
            }

            result = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPT_PATH),
                    "--apk-path",
                    str(apk_path),
                    "--gradle-config",
                    str(gradle_config),
                    "--output",
                    str(output_path),
                    "--tag",
                    "android-v1.1.3",
                    "--repository",
                    "zzlye/poolwatch",
                ],
                input=json.dumps(release, ensure_ascii=False),
                text=True,
                capture_output=True,
                check=False,
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            metadata = json.loads(output_path.read_text(encoding="utf-8"))
            self.assertEqual(
                metadata["downloadUrl"],
                (
                    "https://github.com/zzlye/poolwatch/releases/download/"
                    f"android-v1.1.3/{apk_name}"
                ),
            )
            self.assertEqual(
                metadata["releaseUrl"],
                "https://github.com/zzlye/poolwatch/releases/tag/android-v1.1.3",
            )


if __name__ == "__main__":
    unittest.main()

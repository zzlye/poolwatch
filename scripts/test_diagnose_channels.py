#!/usr/bin/env python3
"""验证渠道巡检只读取必要字段，不泄露凭据或修改数据库。"""
import importlib.util
import json
import sqlite3
import tempfile
import unittest
from contextlib import closing
from contextlib import redirect_stdout
from io import StringIO
from unittest.mock import patch
from pathlib import Path

SCRIPT = Path(__file__).with_name("diagnose_channels.py")
spec = importlib.util.spec_from_file_location("diagnose_channels", SCRIPT)
diagnose = importlib.util.module_from_spec(spec)
spec.loader.exec_module(diagnose)

class DiagnoseChannelsTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / "poolwatch.db"
        with closing(sqlite3.connect(self.path)) as db, db:
            db.execute("CREATE TABLE targets (id TEXT, name TEXT, kind TEXT, enabled INTEGER, status TEXT, failure_count INTEGER, last_error TEXT, last_checked_at TEXT, credentials_enc TEXT)")
            db.executemany("INSERT INTO targets VALUES (?,?,?,?,?,?,?,?,?)", [
                ("a", "正常渠道", "sub2api", 1, "healthy", 0, "", "2026-09-28T12:00:00Z", "PRIVATE_CREDENTIAL"),
                ("b", "登录渠道", "new_api", 1, "error", 3, "站点启用了浏览器验证，请改用网页登录或访问令牌", None, "PRIVATE_CREDENTIAL"),
                ("c", "会话渠道", "new_api", 1, "error", 4, "渠道凭据无效或权限不足", None, "PRIVATE_CREDENTIAL"),
                ("d", "号池渠道", "cliproxyapi", 1, "error", 5, "渠道服务器暂时不可用", None, "PRIVATE_CREDENTIAL"),
                ("e", "余额渠道", "new_api", 1, "warning", 0, "", None, "PRIVATE_CREDENTIAL"),
                ("f", "已关闭渠道", "custom", 0, "error", 20, "PRIVATE_ERROR", None, "PRIVATE_CREDENTIAL"),
            ])

    def test_classifies_independent_channel_failures(self):
        """浏览器验证、凭据拒绝与上游错误必须分开表示。"""
        report = diagnose.inspect_database(self.path)
        self.assertEqual([r["reason"] for r in report["targets"]], ["none", "browser_verification", "credentials_rejected", "upstream_unavailable", "attention", "disabled"])
        self.assertEqual(report["counts"], {"total": 6, "healthy": 1, "attention": 1, "failed": 3, "disabled": 1})

    def test_never_returns_raw_errors_or_credentials(self):
        """报告只输出固定诊断描述，不透传任何凭据和原始错误。"""
        with closing(sqlite3.connect(self.path)) as db, db:
            db.execute("UPDATE targets SET last_error = 'PRIVATE_ERROR', status = 'error' WHERE id = 'a'")
        output = json.dumps(diagnose.inspect_database(self.path), ensure_ascii=False)
        self.assertNotIn("PRIVATE_ERROR", output)
        self.assertNotIn("PRIVATE_CREDENTIAL", output)
        self.assertNotIn("credentials_enc", output)
        self.assertNotIn("last_error", output)
        self.assertEqual(diagnose.inspect_database(self.path)["targets"][0]["reason"], "needs_review")

    def test_keeps_database_bytes_unchanged(self):
        """只读巡检前后主数据库内容必须完全一致。"""
        before = self.path.read_bytes()
        diagnose.inspect_database(self.path)
        self.assertEqual(self.path.read_bytes(), before)

    def test_missing_database_is_not_created(self):
        """路径错误时禁止创建空数据库。"""
        missing = self.path.parent / "missing.db"
        with self.assertRaises(sqlite3.Error):
            diagnose.inspect_database(missing)
        self.assertFalse(missing.exists())

    def test_unknown_status_is_not_healthy(self):
        """未知状态必须提示核查，不能当作恢复正常。"""
        with closing(sqlite3.connect(self.path)) as db, db:
            db.execute("UPDATE targets SET status = 'unknown' WHERE id = 'a'")
        report = diagnose.inspect_database(self.path)
        self.assertEqual(report["targets"][0]["reason"], "needs_review")
        self.assertEqual(report["counts"]["healthy"], 0)

    def test_preserves_snapshot_time_without_claiming_network_probe(self):
        """保存时间用于判断历史结果新旧，不伪装成即时检测。"""
        report = diagnose.inspect_database(self.path)
        self.assertEqual(report["source"], "stored_checks")
        self.assertEqual(report["targets"][0]["last_checked_at"], "2026-09-28T12:00:00Z")

    def test_command_exit_codes(self):
        """监控失败、全部正常与读库失败必须具有不同退出码。"""
        def run(path):
            output = StringIO()
            with patch("sys.argv", [str(SCRIPT), str(path)]), redirect_stdout(output):
                result = diagnose.main()
            return result, json.loads(output.getvalue())
        self.assertEqual(run(self.path)[0], 2)
        with closing(sqlite3.connect(self.path)) as db, db:
            db.execute("UPDATE targets SET status = 'healthy', last_error = ''")
        self.assertEqual(run(self.path)[0], 0)
        missing = self.path.parent / "missing.db"
        code, report = run(missing)
        self.assertEqual(code, 1)
        self.assertIn("error", report)
        self.assertFalse(missing.exists())

if __name__ == "__main__":
    unittest.main()

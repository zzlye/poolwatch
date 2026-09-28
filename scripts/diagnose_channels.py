#!/usr/bin/env python3
"""只读汇总已保存的渠道检测结果，不发起登录或修改渠道配置。"""
import argparse
import json
import sqlite3
from datetime import datetime, timezone
from pathlib import Path

REASONS = {
    "none": "最近一次保存的检测结果正常。",
    "attention": "渠道可检测但存在警告，请查看余额阈值或账号状态。",
    "disabled": "渠道或上游账号已停用，不计入正常渠道。",
    "browser_verification": "站点要求浏览器验证，请使用网页授权或管理访问令牌。",
    "credentials_rejected": "站点拒绝当前凭据，请重新授权并检查账号权限。",
    "upstream_unavailable": "上游服务返回服务器错误，请检查渠道服务或反向代理。",
    "connection_failed": "渠道连接失败，请检查地址、域名解析与网络。",
    "needs_review": "当前结果不足以确认原因，需要检查对应渠道。",
}


def classify(enabled, status, error):
    """仅按明确的错误类别给建议，不把错误文案当作可执行内容。"""
    if not enabled or status == "disabled":
        return "disabled"
    if status == "healthy":
        return "none"
    if status == "warning":
        return "attention"
    error = error or ""
    if "浏览器验证" in error:
        return "browser_verification"
    if "凭据无效或权限不足" in error:
        return "credentials_rejected"
    if "渠道服务器暂时不可用" in error:
        return "upstream_unavailable"
    if any(part in error for part in ("无法连接渠道", "域名解析失败", "请求已取消或超时")):
        return "connection_failed"
    return "needs_review"


def inspect_database(path):
    """按列白名单读取数据库，凭据、地址和原始响应不进入报告。"""
    uri = Path(path).resolve().as_uri() + "?mode=ro"
    connection = sqlite3.connect(uri, uri=True, timeout=5)
    try:
        connection.execute("PRAGMA query_only = ON")
        if connection.execute("PRAGMA quick_check").fetchone()[0] != "ok":
            raise sqlite3.DatabaseError("数据库完整性检查失败")
        rows = connection.execute(
            "SELECT id, name, kind, enabled, status, failure_count, last_error, last_checked_at "
            "FROM targets ORDER BY id"
        ).fetchall()
    finally:
        connection.close()
    counts = {"total": len(rows), "healthy": 0, "attention": 0, "failed": 0, "disabled": 0}
    targets = []
    for target_id, name, kind, enabled, status, failures, error, checked_at in rows:
        reason = classify(enabled, status, error)
        category = {"none": "healthy", "attention": "attention", "disabled": "disabled"}.get(reason, "failed")
        counts[category] += 1
        targets.append({
            "id": target_id, "name": name, "kind": kind, "enabled": bool(enabled),
            "status": status, "failure_count": failures, "last_checked_at": checked_at,
            "reason": reason, "advice": REASONS[reason],
        })
    return {
        "source": "stored_checks", "generated_at": datetime.now(timezone.utc).isoformat(),
        "note": "报告仅汇总历史检测结果，请结合最后检测时间判断；未执行新的网络检测。",
        "database_check": "ok", "counts": counts, "targets": targets,
    }


def main():
    """失败渠道返回 2，读取错误返回 1；不输出异常对象中的路径或内容。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("database", type=Path, help="服务器上的号池监控数据库路径")
    args = parser.parse_args()
    try:
        report = inspect_database(args.database)
    except (sqlite3.Error, OSError, ValueError):
        print(json.dumps({"error": "数据库读取失败，请检查路径、权限和数据库结构。"}, ensure_ascii=False))
        return 1
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 2 if report["counts"]["failed"] else 0


if __name__ == "__main__":
    raise SystemExit(main())

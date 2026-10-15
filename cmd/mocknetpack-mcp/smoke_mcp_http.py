#!/usr/bin/env python3
"""Real HTTP MCP smoke: start/reuse -> configure -> poll mock hit -> restore.

Each run owns a unique observerId and retains that lease through the rule
workflow. Cleanup releases only that observer on the acquired session,
deletes the temporary rule and restores the pre-run enabled-rule snapshot,
including when a tool call fails. Run against an isolated online device;
trigger its selected request after the script prints the enabled rule.

MNP_KEY is required; MNP_URL defaults to http://127.0.0.1:4291/mcp.
MNP_APP/MNP_DID select the device. MNP_HIT_TIMEOUT defaults to 30 seconds.
MNP_SKIP_SESSION=1 keeps the original create/read/delete-only smoke.
Usage: MNP_KEY=<key> python3 smoke_mcp_http.py [trafficId]
Never prints the API key or request/response bodies.
"""
import json
import os
import re
import sys
import time
import urllib.request
import uuid
from contextlib import contextmanager, nullcontext
from datetime import datetime

URL = os.environ.get("MNP_URL", "http://127.0.0.1:4291/mcp")
KEY = os.environ["MNP_KEY"]
APP = os.environ.get("MNP_APP", "com.pixelmuse.dokimo")
DID = os.environ.get("MNP_DID", "0a60bddb58f3f5d52cc2569a877ed7cf")
NOTE = "m12.8 http-mcp smoke"
SKIP_SESSION = os.environ.get("MNP_SKIP_SESSION", "") == "1"
HIT_TIMEOUT = float(os.environ.get("MNP_HIT_TIMEOUT", "30"))
SESSION_TOOLS = [
    "get_capture_status", "start_capture", "list_sessions",
    "renew_capture_observer", "release_capture_observer",
]
PUSH_TOOLS = [
    "list_push_mock_rules", "create_push_mock_rule", "update_push_mock_rule",
    "delete_push_mock_rule", "send_push_mock_rule", "get_longconn_packet_samples",
    "list_longconn_logs", "create_push_mock_rule_from_log", "get_push_mock_rule",
]


def post(payload, session=None):
    req = urllib.request.Request(
        URL,
        data=json.dumps(payload).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + KEY},
    )
    if session:
        req.add_header("Mcp-Session-Id", session)
    with urllib.request.urlopen(req, timeout=10) as r:
        raw = r.read()
        return (json.loads(raw) if raw else None), r.headers.get("Mcp-Session-Id")


def tool_call(session, name, arguments):
    res, _ = post({"jsonrpc": "2.0", "id": 0, "method": "tools/call",
                   "params": {"name": name, "arguments": arguments}}, session)
    text = res["result"]["content"][0]["text"]
    if res["result"].get("isError"):
        raise RuntimeError(f"tool {name} failed: {text}")
    return json.loads(text)


def require(condition, message):
    """Contract assertions remain active even with python -O."""
    if not condition:
        raise RuntimeError(message)


def parse_ts(value):
    """Parse Go RFC3339Nano on Python 3.9, at datetime's microsecond precision."""
    match = re.fullmatch(r"([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})"
                         r"(?:\.([0-9]{1,9}))?(Z|[+-][0-9]{2}:[0-9]{2})", value)
    if not match:
        raise ValueError(f"Invalid RFC3339Nano timestamp: {value!r}")
    seconds, fraction, offset = match.groups()
    # Python 3.9 only accepts 3/6 fractional digits; pad or truncate to six.
    micros = "." + fraction[:6].ljust(6, "0") if fraction else ""
    return datetime.fromisoformat(seconds + micros + ("+00:00" if offset == "Z" else offset))


def enabled_rule_ids(session):
    """Snapshot enabled rules before acquiring the smoke-owned lease."""
    out = tool_call(session, "list_mock_rules", {"app": APP, "did": DID})
    return [r["ruleId"] for r in (out.get("rules") or []) if r.get("enabled")]


def renew_session(sid, capture):
    """Renew the same acquired session/observer and validate lease stability."""
    renewed = tool_call(sid, "renew_capture_observer", capture["args"])
    require(renewed.get("sessionId") == capture["args"]["sessionId"], "renew changed session")
    lease = renewed["lease"]
    require(lease["viewerId"] == capture["viewer_id"], "renew changed viewer")
    require(parse_ts(lease["expiresAt"]) >= parse_ts(capture["lease"]["expiresAt"]),
            "renew moved expiresAt backwards")
    require(renewed["session"]["viewerCount"] == capture["viewer_count"],
            "renew changed viewer count (use an isolated device with stable observers)")
    require(lease["ttlSeconds"] > 0 and lease["recommendedRenewEverySeconds"] > 0,
            "invalid lease timeout")
    capture["lease"] = lease
    capture["renew_at"] = time.monotonic() + lease["recommendedRenewEverySeconds"]
    print("renew: stable viewerCount=", capture["viewer_count"])


def cleanup_session(sid, args, enabled_before):
    """Retry ambiguous release once; reconcile ended state and restore rules.

    Restoration is attempted independently of release/state-query failures.
    A failed post-delete GET can hide a successful last-observer release.
    """
    errors = []
    release_error = None
    for _ in range(2):
        try:
            released = tool_call(sid, "release_capture_observer", args)
            require(released.get("released") is True, "release did not release the smoke lease")
            before, after = released.get("before"), released.get("after")
            if before and after and not released.get("idempotent"):
                expected = 0 if after["status"] == "ended" else before["viewerCount"] - 1
                require(after["viewerCount"] == expected, "release viewer count mismatch")
            print("release: released=true | sessionEnded=", released.get("sessionEnded"))
            release_error = None
            break
        except Exception as exc:
            release_error = exc
            try:
                status = tool_call(sid, "get_capture_status", {"app": APP, "did": DID})
                ended = status.get("latestEndedSession") or {}
                if args.get("sessionId") and ended.get("id") == args["sessionId"] and ended.get("status") == "ended":
                    print("release reconciled: acquired session ended; its leases are cleared")
                    release_error = None
                    break
            except Exception:
                # Still attempt the idempotent release of the same identity.
                pass
    if release_error:
        errors.append(f"smoke lease cleanup failed: {release_error}")

    # Check actual rule state, not the release response's inferred boolean.
    # If listing fails, still try each snapshot rule independently.
    try:
        current = tool_call(sid, "list_mock_rules", {"app": APP, "did": DID})
        enabled_now = {r["ruleId"] for r in current.get("rules", []) if r.get("enabled")}
    except Exception:
        enabled_now = set()
    for rid in enabled_before:
        if rid in enabled_now:
            continue
        try:
            restored = tool_call(sid, "set_mock_rule_enabled",
                                 {"app": APP, "did": DID, "ruleId": rid, "enabled": True})
            require(restored.get("enabled") is True, f"rule restoration failed: {rid}")
        except Exception as exc:
            errors.append(f"rule restoration failed: {rid}: {exc}")
    if errors:
        raise RuntimeError("; ".join(errors))
    print("scene restored:", len(enabled_before), "snapshot rules enabled")


@contextmanager
def capture_session(sid):
    """Own a distinct lease through the full workflow, including failure cleanup."""
    status = tool_call(sid, "get_capture_status", {"app": APP, "did": DID})
    print("status: device=", (status.get("device") or {}).get("status"))
    enabled_before = enabled_rule_ids(sid)
    observer_id = "smoke-" + uuid.uuid4().hex
    args = {"app": APP, "did": DID, "observerId": observer_id}
    # Enter cleanup before acquisition: a lost start response may still have
    # registered this unique observer. Without a known ID, resolve current.
    try:
        first = tool_call(sid, "start_capture", args)
        session_id = (first.get("session") or {}).get("id")
        require(session_id, "start_capture did not return a session id")
        args["sessionId"] = session_id
        require(first.get("sessionSnapshotStale") is False, "start returned a stale snapshot")
        require(isinstance(first.get("created"), bool) and first.get("reused") is (not first["created"]),
                "start created/reused contract broken")
        require(first["observer"]["observerId"] == observer_id, "start changed observer identity")
        require(first["session"]["viewerCount"] >= 1, "start omitted the registered viewer")
        capture = {"args": args, "lease": first["lease"],
                   "viewer_id": first["observer"]["viewerId"],
                   "viewer_count": first["session"]["viewerCount"]}
        require(capture["lease"]["viewerId"] == capture["viewer_id"], "start lease identity mismatch")
        again = tool_call(sid, "start_capture", {"app": APP, "did": DID, "observerId": observer_id})
        require(again.get("sessionSnapshotStale") is False, "reuse returned a stale snapshot")
        require(again.get("created") is False and again.get("reused") is True and
                again["session"]["id"] == session_id, "reuse contract broken")
        require(again["session"]["viewerCount"] == capture["viewer_count"], "reuse changed viewer count")
        require(again["observer"]["viewerId"] == capture["viewer_id"], "reuse changed viewer")
        capture["lease"] = again["lease"]
        print("start/reuse: session=", session_id, "| observer=", observer_id)
        renew_session(sid, capture)
        yield capture
    finally:
        cleanup_session(sid, args, enabled_before)


def check_wait(sid, capture, deadline):
    """Check the wait budget and renew at every synchronous request boundary."""
    require(time.monotonic() < deadline, f"capture smoke traffic wait exceeded {HIT_TIMEOUT}s")
    if time.monotonic() >= capture["renew_at"]:
        renew_session(sid, capture)
    require(time.monotonic() < deadline, f"capture smoke traffic wait exceeded {HIT_TIMEOUT}s")


def latest_traffic_seq(sid, capture):
    """Page the ascending compact stream before enabling, without fetching details.

    The first page's total bounds the snapshot scan even if traffic keeps
    arriving. Counts can differ from seq after deletions, so use actual seq.
    """
    require(HIT_TIMEOUT > 0, "MNP_HIT_TIMEOUT must be positive")
    deadline = time.monotonic() + HIT_TIMEOUT
    cursor, scanned, snapshot_total = 0, 0, None
    while True:
        check_wait(sid, capture, deadline)
        page = tool_call(sid, "get_device_traffic", {"app": APP, "did": DID, "since": cursor, "limit": 500})
        check_wait(sid, capture, deadline)
        require(page.get("sessionId") == capture["args"]["sessionId"], "baseline changed capture session")
        entries = page.get("entries", [])
        if snapshot_total is None:
            snapshot_total = page["total"]
        if not entries:
            return cursor
        latest = max(entry["seq"] for entry in entries)
        require(latest > cursor, "baseline traffic cursor did not advance")
        cursor = latest
        scanned += len(entries)
        if len(entries) < 500 or scanned >= snapshot_total:
            return cursor


def poll_mock_hit(sid, capture, rid, cursor):
    """Require a fresh hit; bound page/detail processing and renew while waiting."""
    require(HIT_TIMEOUT > 0, "MNP_HIT_TIMEOUT must be positive")
    deadline = time.monotonic() + HIT_TIMEOUT
    while True:
        check_wait(sid, capture, deadline)
        page = tool_call(sid, "get_device_traffic", {"app": APP, "did": DID, "since": cursor, "limit": 500})
        check_wait(sid, capture, deadline)
        require(page.get("sessionId") == capture["args"]["sessionId"], "poll changed capture session")
        entries = page.get("entries", [])
        for entry in entries:
            check_wait(sid, capture, deadline)
            cursor = max(cursor, entry.get("seq", 0))
            if entry.get("mocked"):
                detail = tool_call(sid, "get_traffic", {"trafficId": entry["id"]})
                # A slow response must not bypass timeout or the due renewal,
                # even when it happens to contain a matching hit.
                check_wait(sid, capture, deadline)
                if detail.get("mocked") and detail.get("matchedRuleId") == rid:
                    print("mock hit verified: rule=", rid, "| traffic=", entry["id"])
                    return
        if len(entries) < 500:
            now = time.monotonic()
            time.sleep(max(0, min(1, deadline - now, capture["renew_at"] - now)))


def rule_section(sid, capture, traffic_id):
    """Create/read a temporary rule; with a lease, enable and verify its hit."""
    if not traffic_id:
        page = tool_call(sid, "get_device_traffic", {"app": APP, "did": DID})
        candidates = [e for e in page.get("entries", []) if not e.get("mocked")]
        require(candidates, f"无可用真实日志：设备 {APP}/{DID} 当前无流量，请先产生抓包流量或显式传 trafficId。")
        traffic_id = candidates[0]["id"]
    made = tool_call(sid, "create_mock_rule_from_traffic",
                     {"app": APP, "did": DID, "trafficId": traffic_id, "note": NOTE})
    rule = made.get("rule") or made
    rid = rule["id"]
    try:
        got = tool_call(sid, "get_mock_rule", {"app": APP, "did": DID, "ruleId": rid})
        require(got["rule"]["id"] == rid, "rule readback mismatch")
        if capture:
            cursor = latest_traffic_seq(sid, capture)
            enabled = tool_call(sid, "set_mock_rule_enabled",
                                {"app": APP, "did": DID, "ruleId": rid, "enabled": True})
            require(enabled.get("enabled") is True and enabled.get("effective") is True,
                    "temporary rule is not enabled/effective (check conflicting rules)")
            print("enabled rule:", rid, "| request:", rule.get("method"), rule.get("path"), flush=True)
            poll_mock_hit(sid, capture, rid, cursor)
    finally:
        deleted = tool_call(sid, "delete_mock_rule", {"app": APP, "did": DID, "ruleId": rid})
        require(deleted.get("deleted") is True, "failed to clean up temporary rule " + rid)
        print("temporary rule deleted:", rid)


def main():
    res, sid = post({"jsonrpc": "2.0", "id": 1, "method": "initialize",
                     "params": {"protocolVersion": "2024-11-05", "capabilities": {},
                                "clientInfo": {"name": "smoke-http", "version": "1"}}})
    info = res["result"]["serverInfo"]
    print("initialize:", res["result"]["protocolVersion"], "| server:", info["name"], info.get("version"))
    post({"jsonrpc": "2.0", "method": "notifications/initialized"}, sid)
    res, _ = post({"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}}, sid)
    tools = [t["name"] for t in res["result"]["tools"]]
    require(len(tools) == 26 and all(n in tools for n in SESSION_TOOLS + PUSH_TOOLS),
            "expected 26 tools including session + push mock tools")
    print("tools/list:", len(tools), "tools")
    traffic_id = sys.argv[1] if len(sys.argv) > 1 else ""
    with nullcontext(None) if SKIP_SESSION else capture_session(sid) as capture:
        # push mock 只读冒烟：模板列表可查（环境可能无模板，只断言调用成功）。
        push_page = tool_call(sid, "list_push_mock_rules", {"app": APP, "did": DID})
        require(isinstance(push_page, dict) and "rules" in push_page, "list_push_mock_rules malformed response")
        print("push templates listed:", len(push_page.get("rules") or []))
        rule_section(sid, capture, traffic_id)
    print("OK smoke passed")


if __name__ == "__main__":
    main()

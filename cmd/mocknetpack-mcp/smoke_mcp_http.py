#!/usr/bin/env python3
"""MCP HTTP (Streamable) smoke: initialize -> tools/call against the real
gateway (http://<host>:4291/mcp, Authorization: Bearer <key>). Verifies the
per-account passthrough path end to end and cleans up the temporary rule.

Never prints the API key. Prints only field lengths and IDs (no request
bodies). Usage:

  MNP_KEY=<key> python3 smoke_mcp_http.py [trafficId]
  # env: MNP_URL (default http://127.0.0.1:4291/mcp),
  #      MNP_APP / MNP_DID (defaults below)

Exit 0 on success; the temporary rule is deleted via delete_mock_rule.
"""
import json
import os
import sys
import urllib.request

URL = os.environ.get("MNP_URL", "http://127.0.0.1:4291/mcp")
KEY = os.environ["MNP_KEY"]
APP = os.environ.get("MNP_APP", "com.pixelmuse.dokimo")
DID = os.environ.get("MNP_DID", "0a60bddb58f3f5d52cc2569a877ed7cf")
NOTE = "m12.7 http-mcp smoke"


def post(payload, session=None):
    req = urllib.request.Request(
        URL,
        data=json.dumps(payload).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + KEY},
    )
    if session:
        req.add_header("Mcp-Session-Id", session)
    with urllib.request.urlopen(req) as r:
        raw = r.read()
        return (json.loads(raw) if raw else None), r.headers.get("Mcp-Session-Id")


def tool_call(session, name, arguments):
    res, _ = post({"jsonrpc": "2.0", "id": 0, "method": "tools/call",
                   "params": {"name": name, "arguments": arguments}}, session)
    text = res["result"]["content"][0]["text"]
    if res["result"].get("isError"):
        raise RuntimeError(f"tool {name} failed: {text}")
    return json.loads(text)


def main():
    res, sid = post({"jsonrpc": "2.0", "id": 1, "method": "initialize",
                     "params": {"protocolVersion": "2024-11-05", "capabilities": {},
                                "clientInfo": {"name": "smoke-http", "version": "1"}}})
    print("initialize:", res["result"]["protocolVersion"], "| server:", res["result"]["serverInfo"]["name"])
    post({"jsonrpc": "2.0", "method": "notifications/initialized"}, sid)

    res, _ = post({"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}}, sid)
    tools = [t["name"] for t in res["result"]["tools"]]
    print("tools/list:", len(tools), "tools")

    traffic_id = sys.argv[1] if len(sys.argv) > 1 else ""
    if not traffic_id:
        # No hard-coded id: use the newest non-mocked log from the device.
        d = tool_call(sid, "get_device_traffic", {"app": APP, "did": DID})
        candidates = [e for e in (d.get("entries") or []) if not e.get("mocked")]
        if not candidates:
            sys.exit(f"无可用真实日志：设备 {APP}/{DID} 当前无流量。请先让设备产生抓包流量后重跑，或显式传 trafficId 参数。")
        traffic_id = candidates[0]["id"]
    print("using traffic:", traffic_id)

    d = tool_call(sid, "create_mock_rule_from_traffic",
                  {"app": APP, "did": DID, "trafficId": traffic_id, "note": NOTE})
    rule = d.get("rule") or d
    src = rule.get("source") or {}
    rid = rule["id"]
    print("created rule:", rid, "| enabled:", rule.get("enabled"))
    print("decoded req/resp:", len(src.get("requestBodyDecoded") or ""), len(src.get("responseBodyDecoded") or ""),
          "| base64:", len(src.get("requestBodyBase64") or ""), len(src.get("responseBodyBase64") or ""))

    g = tool_call(sid, "get_mock_rule", {"app": APP, "did": DID, "ruleId": rid})
    gsrc = (g.get("rule") or {}).get("source") or {}
    print("re-read decoded req/resp:", len(gsrc.get("requestBodyDecoded") or ""), len(gsrc.get("responseBodyDecoded") or ""))

    deleted = tool_call(sid, "delete_mock_rule", {"app": APP, "did": DID, "ruleId": rid}).get("deleted")
    print("delete:", deleted)
    if not deleted:
        sys.exit("failed to clean up temporary rule " + rid)
    print("OK smoke passed; RID=", rid)


if __name__ == "__main__":
    main()

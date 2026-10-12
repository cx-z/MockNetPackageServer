"""Focused smoke failure/cleanup regression tests; no network or real API key."""
import copy
import os
import unittest
from datetime import datetime, timedelta, timezone
from unittest.mock import patch

with patch.dict(os.environ, {"MNP_KEY": "test-only"}):
    import smoke_mcp_http as smoke


class TimestampTests(unittest.TestCase):
    def test_rfc3339nano_all_fraction_precisions_and_offsets(self):
        cases = [("", 0), ("1", 100000), ("12", 120000), ("123", 123000),
                 ("1234", 123400), ("47843", 478430), ("123456", 123456),
                 ("1234567", 123456), ("12345678", 123456), ("123456789", 123456)]
        for fraction, micros in cases:
            for suffix, minutes in [("Z", 0), ("+08:00", 480), ("-05:30", -330)]:
                with self.subTest(fraction=fraction, suffix=suffix):
                    text = "2026-10-12T09:00:51" + ("." + fraction if fraction else "") + suffix
                    expected = datetime(2026, 10, 12, 9, 0, 51, micros,
                                        tzinfo=timezone(timedelta(minutes=minutes)))
                    self.assertEqual(smoke.parse_ts(text), expected)

    def test_invalid_timestamp_is_rejected(self):
        for value in ["2026-10-12T09:00:51", "2026-10-12T09:00:51.Z",
                      "2026-10-12T09:00:51.1234567890Z", "2026-13-12T09:00:51Z"]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                smoke.parse_ts(value)


class SmokeCleanupTests(unittest.TestCase):
    def setUp(self):
        quiet = patch("builtins.print")
        quiet.start()
        self.addCleanup(quiet.stop)
        self.calls = []
        self.args = None
        self.fault = None
        self.fail_release = False
        self.ended = False
        self.restored = []
        self.lease = {"viewerId": "smoke-viewer", "ttlSeconds": 120,
                      "recommendedRenewEverySeconds": 60,
                      "expiresAt": "2026-10-12T10:02:00Z"}

    def call(self, sid, name, args):
        """Inject failures while recording cleanup identity and operation order."""
        self.calls.append((name, copy.deepcopy(args)))
        if name == "get_capture_status":
            return {"device": {"status": "capturing"}, "latestEndedSession":
                    {"id": "owned-session", "status": "ended"} if self.ended else None}
        if name == "list_mock_rules":
            return {"rules": [{"ruleId": "existing", "enabled": len(self.calls) <= 2}]}
        if name == "start_capture":
            reused = self.args is not None
            self.args = copy.deepcopy(args)
            if self.fault == "start":
                raise RuntimeError("lost start response")
            return {"created": not reused, "reused": reused, "sessionSnapshotStale": False,
                    "session": {"id": "owned-session", "viewerCount": 1},
                    "observer": {"observerId": args["observerId"], "viewerId": "smoke-viewer"},
                    "lease": self.lease.copy()}
        if name == "renew_capture_observer":
            if self.fault == "renew":
                raise RuntimeError("renew failed")
            return {"sessionId": "owned-session", "lease": self.lease.copy(),
                    "session": {"viewerCount": 99 if self.fault == "count" else 1}}
        if name == "release_capture_observer":
            if self.fail_release:
                raise RuntimeError("delete failed")
            if self.fault == "post-delete":
                self.ended = True
                raise RuntimeError("post-delete GET failed")
            return {"released": self.fault != "not-released", "sessionEnded": True,
                    "before": {"status": "capturing", "viewerCount": 1},
                    "after": {"status": "ended", "viewerCount": 0}}
        if name == "set_mock_rule_enabled":
            self.restored.append(args["ruleId"])
            return {"enabled": True}
        if name == "delete_mock_rule":
            return {"deleted": True}
        raise AssertionError(name)

    def run_session(self):
        with patch.object(smoke, "tool_call", side_effect=self.call):
            with smoke.capture_session("mcp-session") as capture:
                self.assertEqual(capture["args"]["sessionId"], "owned-session")
                self.calls.append(("workflow", {}))

    def test_unique_observer_and_lease_retained_through_workflow(self):
        self.run_session()
        observer1 = self.args["observerId"]
        self.assertTrue(observer1.startswith("smoke-"))
        names = [name for name, _ in self.calls]
        self.assertLess(names.index("workflow"), names.index("release_capture_observer"))
        for name, args in self.calls:
            if name in ("renew_capture_observer", "release_capture_observer"):
                self.assertEqual(args["sessionId"], "owned-session")
                self.assertEqual(args["observerId"], observer1)
        self.args = None
        self.run_session()
        self.assertNotEqual(observer1, self.args["observerId"])

    def test_renew_error_releases_and_restores(self):
        self.fault = "renew"
        with self.assertRaisesRegex(RuntimeError, "renew failed"):
            self.run_session()
        self.assertIn("release_capture_observer", [name for name, _ in self.calls])
        self.assertEqual(self.restored, ["existing"])

    def test_lost_start_response_cleans_up_unique_observer(self):
        self.fault = "start"
        with self.assertRaisesRegex(RuntimeError, "lost start response"):
            self.run_session()
        release_args = next(args for name, args in self.calls if name == "release_capture_observer")
        self.assertEqual(release_args["observerId"], self.args["observerId"])
        self.assertNotIn("sessionId", release_args)
        self.assertEqual(self.restored, ["existing"])

    def test_post_delete_failure_reconciles_and_restores(self):
        self.fault = "post-delete"
        self.run_session()
        self.assertTrue(self.ended)
        self.assertEqual(self.restored, ["existing"])

    def test_release_failure_still_attempts_restoration(self):
        self.fail_release = True
        with self.assertRaisesRegex(RuntimeError, "lease cleanup failed"):
            self.run_session()
        releases = [name for name, _ in self.calls if name == "release_capture_observer"]
        self.assertEqual(len(releases), 2)
        self.assertEqual(self.restored, ["existing"])

    def test_changed_count_and_false_release_fail(self):
        for fault, message in [("count", "renew changed viewer count"),
                               ("not-released", "release did not release")]:
            with self.subTest(fault=fault):
                self.setUp()
                self.fault = fault
                with self.assertRaisesRegex(RuntimeError, message):
                    self.run_session()
                self.assertEqual(self.restored, ["existing"])

    def test_workflow_failure_cleans_up_session(self):
        with patch.object(smoke, "tool_call", side_effect=self.call):
            with self.assertRaisesRegex(RuntimeError, "workflow failed"):
                with smoke.capture_session("mcp-session"):
                    raise RuntimeError("workflow failed")
        self.assertIn("release_capture_observer", [name for name, _ in self.calls])
        self.assertEqual(self.restored, ["existing"])

    def test_poll_error_deletes_rule_before_releasing_lease(self):
        def rule_call(sid, name, args):
            if name == "create_mock_rule_from_traffic":
                return {"rule": {"id": "temporary", "method": "GET", "path": "/feed"}}
            if name == "get_mock_rule":
                return {"rule": {"id": "temporary"}}
            if name == "get_device_traffic":
                return {"sessionId": "owned-session", "entries": [], "total": 0}
            if name == "set_mock_rule_enabled" and args["ruleId"] == "temporary":
                return {"enabled": True, "effective": True}
            return self.call(sid, name, args)

        with patch.object(smoke, "tool_call", side_effect=rule_call), \
                patch.object(smoke, "poll_mock_hit", side_effect=RuntimeError("poll failed")):
            with self.assertRaisesRegex(RuntimeError, "poll failed"):
                with smoke.capture_session("mcp-session") as capture:
                    smoke.rule_section("mcp-session", capture, "traffic")
        names = [name for name, _ in self.calls]
        self.assertLess(names.index("delete_mock_rule"), names.index("release_capture_observer"))
        self.assertEqual(self.restored, ["existing"])

    def test_poll_requires_matching_rule_and_ignores_old_hits(self):
        pages = iter([
            {"sessionId": "owned-session", "entries": [{"id": "other", "seq": 8, "mocked": True}]},
            {"sessionId": "owned-session", "entries": [{"id": "ours", "seq": 9, "mocked": True}]},
        ])
        cursors = []
        def poll_call(sid, name, args):
            if name == "get_device_traffic":
                cursors.append(args["since"])
                return next(pages)
            return {"mocked": True, "matchedRuleId": "temporary" if args["trafficId"] == "ours" else "other-rule"}
        capture = {"args": {"sessionId": "owned-session"}, "renew_at": float("inf")}
        with patch.object(smoke, "tool_call", side_effect=poll_call), patch.object(smoke.time, "sleep"):
            smoke.poll_mock_hit("mcp-session", capture, "temporary", 7)
        self.assertEqual(cursors, [7, 8])


    def test_slow_page_stops_at_deadline_and_renews_between_details(self):
        # Reproduce B's 180 one-second details without 180 seconds of sleeping.
        for late_hit in [False, True]:
            with self.subTest(late_hit=late_hit):
                clock = {"now": 0, "details": 0, "renews": []}
                capture = {"args": {"sessionId": "owned-session"}, "renew_at": 10}
                def timed_call(sid, name, args):
                    if name == "get_device_traffic":
                        return {"sessionId": "owned-session", "entries": [
                            {"id": str(n), "seq": n + 1, "mocked": True} for n in range(180)]}
                    clock["now"] += 1
                    clock["details"] += 1
                    # Even a hit returned exactly at the deadline is too late.
                    matched = "temporary" if late_hit and clock["details"] == 30 else "unrelated"
                    return {"mocked": True, "matchedRuleId": matched}
                def renew(sid, held):
                    clock["renews"].append(clock["now"])
                    held["renew_at"] = clock["now"] + 10
                with patch.object(smoke, "HIT_TIMEOUT", 30), \
                        patch.object(smoke, "tool_call", side_effect=timed_call), \
                        patch.object(smoke, "renew_session", side_effect=renew), \
                        patch.object(smoke.time, "monotonic", side_effect=lambda: clock["now"]), \
                        patch.object(smoke.time, "sleep", side_effect=lambda delay: clock.update(now=clock["now"] + delay)):
                    with self.assertRaisesRegex(RuntimeError, "traffic wait exceeded 30s"):
                        smoke.poll_mock_hit("mcp", capture, "temporary", 0)
                self.assertEqual(clock["now"], 30)
                self.assertEqual(clock["details"], 30)
                self.assertEqual(clock["renews"], [10, 20])

    def test_slow_page_renews_and_can_find_a_later_hit(self):
        clock = {"now": 0, "details": 0, "renews": []}
        capture = {"args": {"sessionId": "owned-session"}, "renew_at": 10}
        def timed_call(sid, name, args):
            if name == "get_device_traffic":
                return {"sessionId": "owned-session", "entries": [
                    {"id": str(n), "seq": n + 1, "mocked": True} for n in range(180)]}
            clock["now"] += 1
            clock["details"] += 1
            return {"mocked": True, "matchedRuleId": "temporary" if clock["details"] == 75 else "other"}
        def renew(sid, held):
            clock["renews"].append(clock["now"])
            held["renew_at"] = clock["now"] + 10
        with patch.object(smoke, "HIT_TIMEOUT", 90), \
                patch.object(smoke, "tool_call", side_effect=timed_call), \
                patch.object(smoke, "renew_session", side_effect=renew), \
                patch.object(smoke.time, "monotonic", side_effect=lambda: clock["now"]):
            smoke.poll_mock_hit("mcp", capture, "temporary", 0)
        self.assertEqual(clock["details"], 75)
        self.assertEqual(clock["renews"], [10, 20, 30, 40, 50, 60, 70])

    def test_baseline_pages_actual_seq_gaps_and_bounds_continuous_arrivals(self):
        # 1,000 retained rows with gaps after deletions: count is not latest seq.
        # Subsequent arrivals must not cause an unbounded scan before enabling.
        cursors = []
        clock = {"now": 0, "renews": []}
        capture = {"args": {"sessionId": "owned-session"}, "renew_at": 1}
        def page_call(sid, name, args):
            self.assertEqual(name, "get_device_traffic")
            cursors.append(args["since"])
            clock["now"] += 1
            first = args["since"] == 0
            start = 2 if first else 1002
            return {"sessionId": "owned-session", "total": 1000,
                    "entries": [{"seq": n} for n in range(start, start + 1000, 2)]}
        def renew(sid, held):
            clock["renews"].append(clock["now"])
            held["renew_at"] = clock["now"] + 1
        with patch.object(smoke, "tool_call", side_effect=page_call), \
                patch.object(smoke, "renew_session", side_effect=renew), \
                patch.object(smoke.time, "monotonic", side_effect=lambda: clock["now"]):
            cursor = smoke.latest_traffic_seq("mcp", capture)
        self.assertEqual(cursor, 2000)
        self.assertEqual(cursors, [0, 1000])
        self.assertEqual(clock["renews"], [1, 2])

    def test_baseline_checks_timeout_on_a_slow_page(self):
        clock = {"now": 0}
        capture = {"args": {"sessionId": "owned-session"}, "renew_at": float("inf")}
        def page_call(sid, name, args):
            clock["now"] = 30
            return {"sessionId": "owned-session", "total": 1, "entries": [{"seq": 1}]}
        with patch.object(smoke, "HIT_TIMEOUT", 30), \
                patch.object(smoke, "tool_call", side_effect=page_call), \
                patch.object(smoke.time, "monotonic", side_effect=lambda: clock["now"]):
            with self.assertRaisesRegex(RuntimeError, "traffic wait exceeded 30s"):
                smoke.latest_traffic_seq("mcp", capture)

    def test_rule_workflow_polls_after_all_1000_baseline_rows(self):
        cursors = []
        order = []
        def rule_call(sid, name, args):
            order.append(name)
            if name == "create_mock_rule_from_traffic":
                return {"rule": {"id": "temporary", "method": "GET", "path": "/feed"}}
            if name == "get_mock_rule":
                return {"rule": {"id": "temporary"}}
            if name == "get_device_traffic":
                cursor = args["since"]
                cursors.append(cursor)
                if cursor == 1000:
                    return {"sessionId": "owned-session", "total": 1,
                            "entries": [{"id": "new-hit", "seq": 1001, "mocked": True}]}
                return {"sessionId": "owned-session", "total": 1000 - cursor,
                        "entries": [{"id": "old", "seq": n, "mocked": True}
                                    for n in range(cursor + 1, cursor + 501)]}
            if name == "get_traffic":
                self.assertEqual(args["trafficId"], "new-hit", "do not inspect old details")
                return {"mocked": True, "matchedRuleId": "temporary"}
            if name == "set_mock_rule_enabled":
                return {"enabled": True, "effective": True}
            return self.call(sid, name, args)
        capture = {"args": {"sessionId": "owned-session"}, "renew_at": float("inf")}
        with patch.object(smoke, "tool_call", side_effect=rule_call):
            smoke.rule_section("mcp", capture, "source-traffic")
        self.assertEqual(cursors, [0, 500, 1000])
        self.assertEqual(order.count("get_traffic"), 1)
        self.assertLess(order.index("set_mock_rule_enabled"), order.index("get_traffic"))


if __name__ == "__main__":
    unittest.main()

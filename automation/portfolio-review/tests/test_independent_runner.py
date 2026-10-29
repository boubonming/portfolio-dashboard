import importlib.util
import json
from datetime import datetime, timezone
from pathlib import Path


MODULE_PATH = Path(__file__).parents[1] / "portfolio_dashboard_review.py"
spec = importlib.util.spec_from_file_location("independent_runner", MODULE_PATH)
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


NOW = "2026-01-02T12:00:00.000000000Z"


def holding(key, instrument, symbol, fresh=True):
    return {
        "portfolio_id": "portfolio-test",
        "snapshot_id": "snapshot-test",
        "holding_id": key,
        "instrument_id": instrument,
        "symbol": symbol,
        "name": symbol + " issuer",
        "quantity": "2",
        "currency": "USD",
        "unit_cost": "3",
        "cost_basis": "6",
        "latest_price": "5" if fresh else None,
        "latest_value": "10" if fresh else None,
        "reporting_value": "13" if fresh else None,
        "quote_source": "fixture" if fresh else "",
        "market_at": NOW if fresh else "",
        "freshness_state": "fresh" if fresh else "missing",
        "freshness_age_seconds": "0" if fresh else None,
    }


def snapshot(*holdings):
    return {
        "portfolio_id": "portfolio-test",
        "snapshot_id": "snapshot-test",
        "calculation_version": "review.v1",
        "state": "incomplete" if any(h["freshness_state"] != "fresh" for h in holdings) else "complete",
        "created_at": NOW,
        "as_of": NOW,
        "complete": not any(h["freshness_state"] != "fresh" for h in holdings),
        "missing_dependencies": ["quote:instrument-missing"] if len(holdings) > 1 else [],
        "stale_dependencies": [],
        "invalid_dependencies": [],
        "holdings": list(holdings),
    }


def evidence(key):
    return [{
        "evidence_id": key + "-e1",
        "url": "https://www.sec.gov/ixviewer/documents/fixture-" + key,
        "title": key + " current filing",
        "accessed_at": NOW,
        "availability": "available",
        "freshness": "current",
        "excerpt": key + " disclosed operating results and risks.",
    }]


def opinion():
    return {
        "recommendation": "hold",
        "rationale": "The issuer has a supported operating thesis.",
        "risks": ["Execution risk."],
        "counterarguments": ["The evidence may not persist."],
        "conditions": ["Reassess on new disclosure."],
        "sourced_facts": [{"statement": "The issuer disclosed operating results.", "evidence_ids": ["a-e1"]}],
        "model_opinion": "The supplied evidence supports a cautious assessment.",
    }


def config():
    return {"model": "fixture-model", "max_snapshot_age_hours": 24, "submission_enabled": True}


def model_for_target(snapshot_value, metrics, evidence_value, config_value, target=None):
    result = opinion()
    result["sourced_facts"] = [{
        "statement": target["symbol"] + " disclosed operating results.",
        "evidence_ids": [evidence_value[0]["evidence_id"]],
    }]
    return result


def test_each_lot_gets_an_independent_payload_and_missing_price_keeps_qualitative_assessment(tmp_path):
    holdings = [holding("lot-a", "instrument-a", "ALFA"), holding("lot-b", "instrument-b", "BETA", fresh=False)]
    snap = snapshot(*holdings)
    root = tmp_path / "review-root"
    run = "weekly-independent"
    runner.atomic_json(root / "runs" / run / "snapshot.json", snap)
    manifest = {
        "run_id": run,
        "phase": "prepared",
        "snapshot_state": snap["state"],
        "holding_count": 2,
        "target_count": 2,
        "coverage_complete": False,
        "targets": [
            {"target_key": h["holding_id"], "instrument_key": h["instrument_id"], "symbol": h["symbol"], "status": "pending"}
            for h in holdings
        ],
    }
    runner.atomic_json(root / "runs" / run / "manifest.json", manifest)
    result = runner.draft_targets(
        root, run,
        {"lot-a": evidence("a"), "lot-b": evidence("b")},
        config(), model_fn=model_for_target, generated_at=NOW,
        now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc),
    )
    assert result["target_count"] == 2
    assert {x["target_key"] for x in result["outcomes"]} == {"lot-a", "lot-b"}
    assert not result["coverage_complete"]

    payload_a = json.loads((root / "runs" / run / "targets" / "lot-a" / "pending.json").read_text())["payload"]
    payload_b = json.loads((root / "runs" / run / "targets" / "lot-b" / "pending.json").read_text())["payload"]
    assert payload_a["status"] == "complete"
    assert payload_a["scope"] == {"type": "holding", "holding_ids": ["lot-a"], "instrument_ids": ["instrument-a"]}
    assert payload_b["status"] == "incomplete"
    assert payload_b["recommendation"] == "no_recommendation"
    assert "qualitative business, thesis, disclosure, and risk analysis remains separate" in payload_b["rationale"]
    assert payload_b["scope"]["holding_ids"] == ["lot-b"]
    assert payload_b["metrics"] == runner.target_metrics(snap, holdings[1])


def test_same_symbol_lots_remain_distinct_targets(tmp_path):
    holdings = [holding("lot-one", "instrument-one", "SAME"), holding("lot-two", "instrument-two", "SAME")]
    snap = snapshot(*holdings)
    root = tmp_path / "root"
    run = "same-symbol"
    client = FakeClient(snap)
    manifest = runner.prepare(client, root, run, now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc))
    assert manifest["target_count"] == 2
    assert [x["target_key"] for x in manifest["targets"]] == ["lot-one", "lot-two"]

    evidence_map = {key: evidence(key[4:]) for key in ("lot-one", "lot-two")}
    result = runner.draft_targets(
        root, run, evidence_map, config(), model_fn=model_for_target, generated_at=NOW,
        now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc),
    )
    assert len(result["outcomes"]) == 2
    for key, instrument in (("lot-one", "instrument-one"), ("lot-two", "instrument-two")):
        payload = json.loads((root / "runs" / run / "targets" / key / "pending.json").read_text())["payload"]
        assert payload["scope"]["holding_ids"] == [key]
        assert payload["scope"]["instrument_ids"] == [instrument]


class FakeClient:
    def __init__(self, snap, fail_target=None):
        self.portfolio = snap["portfolio_id"]
        self.config = config()
        self.base = "/api/v1/portfolios/" + self.portfolio
        self.snapshot = snap
        self.fail_target = fail_target
        self.posts = []
        self.reads = []

    def request(self, method, path, payload=None, key=None):
        if method == "GET" and path.endswith("/snapshots/latest"):
            return self.snapshot
        if method == "POST":
            target = payload["scope"]["holding_ids"][0]
            if target == self.fail_target:
                raise ValueError("synthetic submission failure")
            self.posts.append((target, key, payload))
            return {"review_id": "review-" + target, "portfolio_id": self.portfolio}
        if method == "GET" and "/reviews/" in path:
            target = path.rsplit("/", 1)[1].replace("review-", "")
            payload = next(item[2] for item in self.posts if item[0] == target)
            self.reads.append(target)
            result = dict(payload)
            result.update({"review_id": "review-" + target, "portfolio_id": self.portfolio,
                           "lifecycle": {"outdated": False}})
            return result
        raise AssertionError((method, path))


def test_one_target_failure_preserves_success_and_retry_state(tmp_path, monkeypatch):
    holdings = [holding("lot-a", "instrument-a", "ALFA"), holding("lot-b", "instrument-b", "BETA")]
    snap = snapshot(*holdings)
    root = tmp_path / "root"
    run = "failure-retry"
    client = FakeClient(snap)
    runner.prepare(client, root, run, now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc))
    runner.draft_targets(
        root, run, {"lot-a": evidence("a"), "lot-b": evidence("b")}, config(),
        model_fn=model_for_target, generated_at=NOW,
        now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc),
    )
    monkeypatch.setattr(runner, "utc_now", lambda: datetime(2026, 1, 2, 12, tzinfo=timezone.utc))
    monkeypatch.setattr(runner, "now_text", lambda: NOW)

    failing = FakeClient(snap, fail_target="lot-b")
    first = runner.submit_targets(failing, root, run, approved=True)
    assert not first["coverage_complete"]
    assert {target for target, _, _ in failing.posts} == {"lot-a"}
    assert json.loads((root / "runs" / run / "targets" / "lot-b" / "progress.json").read_text())["status"] == "failed"
    attempted = json.loads((root / "runs" / run / "targets" / "lot-b" / "attempted.json").read_text())

    retry = FakeClient(snap)
    second = runner.submit_targets(retry, root, run, approved=True)
    assert second["coverage_complete"]
    assert {target for target, _, _ in retry.posts} == {"lot-b"}
    assert json.loads((root / "runs" / run / "targets" / "lot-b" / "attempted.json").read_text()) == attempted
    assert len(retry.reads) == 1

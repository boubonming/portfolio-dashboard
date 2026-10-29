import importlib.util
import json
import pytest
from datetime import datetime, timezone
from pathlib import Path
from shutil import copy2


MODULE_PATH = Path(__file__).parents[1] / "portfolio_dashboard_review.py"
spec = importlib.util.spec_from_file_location("independent_runner", MODULE_PATH)
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


NOW = "2026-01-02T12:00:00.000000000Z"


def holding(key, instrument, symbol, fresh=True, asset_type=None):
    result = {
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
    if asset_type is not None:
        result["asset_type"] = asset_type
    return result


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


def legacy_model(snapshot_value, metrics, evidence_value, config_value):
    result = opinion()
    result["sourced_facts"] = [{
        "statement": "The portfolio disclosed operating results.",
        "evidence_ids": [evidence_value[0]["evidence_id"]],
    }]
    return result


def test_legacy_portfolio_draft_uses_schema_beside_installed_runner(tmp_path):
    installed = tmp_path / "installed-runner"
    installed.mkdir()
    copy2(MODULE_PATH, installed / MODULE_PATH.name)
    copy2(MODULE_PATH.with_name("review-schema.json"), installed / "review-schema.json")
    spec = importlib.util.spec_from_file_location("installed_runner", installed / MODULE_PATH.name)
    installed_runner = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(installed_runner)

    assert installed_runner.DEFAULT_SCHEMA == installed / "review-schema.json"

    snap = snapshot(holding("lot-a", "instrument-a", "ALFA"))
    root = tmp_path / "review-root"
    run = "legacy-portfolio"
    directory = root / "runs" / run
    installed_runner.atomic_json(directory / "snapshot.json", snap)
    installed_runner.atomic_json(directory / "manifest.json", {"run_id": run, "phase": "prepared"})

    result = installed_runner.draft(
        root, run, evidence("a"), config(), model_fn=legacy_model,
        generated_at=NOW, now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc),
    )

    assert result == {
        "run_id": run, "phase": "drafted", "review_status": "complete", "submitted": False,
    }
    pending = json.loads((directory / "pending.json").read_text())
    assert isinstance(pending["payload"]["metrics"], list)
    assert pending["payload"]["metrics"] == installed_runner.snapshot_metrics(snap)
    assert pending["payload"]["scope"] == {
        "type": "portfolio", "holding_ids": [], "instrument_ids": [],
    }


def test_each_lot_gets_an_independent_payload_and_missing_price_keeps_qualitative_assessment(tmp_path):
    holdings = [holding("lot-a", "instrument-a", "ALFA"), holding("lot-b", "instrument-b", "BETA", fresh=False)]
    snap = snapshot(*holdings)
    root = tmp_path / "review-root"
    run = "weekly-independent"
    runner.atomic_json(root / "runs" / run / "snapshot.json", snap)
    manifest = {
        "run_id": run,
        "phase": "prepared",
        "snapshot_id": snap["snapshot_id"],
        "snapshot_sha256": runner.fingerprint(snap),
        "calculation_version": snap["calculation_version"],
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
    def __init__(self, snap, fail_target=None, server=None):
        self.portfolio = snap["portfolio_id"]
        self.config = config()
        self.base = "/api/v1/portfolios/" + self.portfolio
        self.snapshot = snap
        self.fail_target = fail_target
        self.posts = []
        self.reads = []
        self.corrupt_readback = False
        self.server = server if server is not None else {}

    def request(self, method, path, payload=None, key=None):
        if method == "GET" and path.endswith("/snapshots/latest"):
            return self.snapshot
        if method == "POST":
            target = payload["scope"]["holding_ids"][0]
            if target == self.fail_target:
                raise ValueError("synthetic submission failure")
            self.posts.append((target, key, payload))
            self.server[target] = (key, payload)
            return {"review_id": "review-" + target, "portfolio_id": self.portfolio}
        if method == "GET" and "/reviews/" in path:
            target = path.rsplit("/", 1)[1].replace("review-", "")
            payload = self.server[target][1]
            self.reads.append(target)
            result = dict(payload)
            result.update({"review_id": "review-" + target, "portfolio_id": self.portfolio,
                           "lifecycle": {"outdated": False}})
            if self.corrupt_readback:
                result["rationale"] = "corrupted read-back"
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

    retry = FakeClient(snap, server=failing.server)
    second = runner.submit_targets(retry, root, run, approved=True)
    assert second["coverage_complete"]
    assert {target for target, _, _ in retry.posts} == {"lot-b"}
    assert json.loads((root / "runs" / run / "targets" / "lot-b" / "attempted.json").read_text()) == attempted
    assert len(retry.reads) == 2


def _draft_one(root, run, client, model_fn=model_for_target):
    runner.prepare(client, root, run, now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc))
    runner.draft_targets(
        root, run, {"lot-a": evidence("a")}, config(), model_fn=model_fn,
        generated_at=NOW, now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc),
    )


def test_forged_receipt_cannot_verify_wrong_target_or_complete_coverage(tmp_path, monkeypatch):
    snap = snapshot(holding("lot-a", "instrument-a", "ALFA"))
    root = tmp_path / "root"
    client = FakeClient(snap)
    _draft_one(root, "forged-receipt", client)
    receipt_path = root / "runs" / "forged-receipt" / "targets" / "lot-a" / "receipt.json"
    runner.atomic_json(receipt_path, {"target_key": "lot-other", "phase": "verified", "review_id": "review-forged"})
    result = runner.submit_targets(client, root, "forged-receipt", approved=True)
    assert not result["coverage_complete"]
    assert result["outcomes"] == [{"target_key": "lot-a", "status": "failed"}]


def test_exact_receipt_readback_avoids_duplicate_post(tmp_path, monkeypatch):
    snap = snapshot(holding("lot-a", "instrument-a", "ALFA"))
    root = tmp_path / "root"
    client = FakeClient(snap)
    _draft_one(root, "exact-receipt", client)
    monkeypatch.setattr(runner, "utc_now", lambda: datetime(2026, 1, 2, 12, tzinfo=timezone.utc))
    monkeypatch.setattr(runner, "now_text", lambda: NOW)
    first = runner.submit_targets(client, root, "exact-receipt", approved=True)
    assert first["coverage_complete"]
    post_count = len(client.posts)
    read_count = len(client.reads)
    second = runner.submit_targets(client, root, "exact-receipt", approved=True)
    assert second["coverage_complete"]
    assert len(client.posts) == post_count
    assert len(client.reads) == read_count + 1


def test_receipt_readback_mismatch_is_rejected(tmp_path, monkeypatch):
    snap = snapshot(holding("lot-a", "instrument-a", "ALFA"))
    root = tmp_path / "root"
    client = FakeClient(snap)
    _draft_one(root, "corrupt-readback", client)
    monkeypatch.setattr(runner, "utc_now", lambda: datetime(2026, 1, 2, 12, tzinfo=timezone.utc))
    monkeypatch.setattr(runner, "now_text", lambda: NOW)
    assert runner.submit_targets(client, root, "corrupt-readback", approved=True)["coverage_complete"]
    client.corrupt_readback = True
    result = runner.submit_targets(client, root, "corrupt-readback", approved=True)
    assert not result["coverage_complete"]
    assert result["outcomes"] == [{"target_key": "lot-a", "status": "failed"}]


def test_existing_prepare_reconciles_corrupt_exact_id_readback(tmp_path, monkeypatch):
    snap = snapshot(holding("lot-a", "instrument-a", "ALFA"))
    root = tmp_path / "root"
    client = FakeClient(snap)
    _draft_one(root, "prepare-corrupt-readback", client)
    monkeypatch.setattr(runner, "utc_now", lambda: datetime(2026, 1, 2, 12, tzinfo=timezone.utc))
    monkeypatch.setattr(runner, "now_text", lambda: NOW)
    assert runner.submit_targets(client, root, "prepare-corrupt-readback", approved=True)["successful_review_complete"]
    client.corrupt_readback = True

    result = runner.prepare(
        client, root, "prepare-corrupt-readback",
        now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc),
    )

    assert not result["coverage_complete"]
    assert not result["successful_review_complete"]
    assert result["phase"] != "verified"
    assert [(item["target_key"], item["status"]) for item in result["targets"]] == [("lot-a", "pending_readback")]
    assert result["targets"][0]["reason"] == "readback_invalid"
    assert client.reads[-1] == "lot-a"


def test_existing_prepare_reconciles_tampered_or_missing_receipt_and_pending(tmp_path, monkeypatch):
    snap = snapshot(holding("lot-a", "instrument-a", "ALFA"))
    root = tmp_path / "root"
    client = FakeClient(snap)
    _draft_one(root, "prepare-tampered-artifacts", client)
    monkeypatch.setattr(runner, "utc_now", lambda: datetime(2026, 1, 2, 12, tzinfo=timezone.utc))
    monkeypatch.setattr(runner, "now_text", lambda: NOW)
    assert runner.submit_targets(client, root, "prepare-tampered-artifacts", approved=True)["coverage_complete"]
    target_dir = root / "runs" / "prepare-tampered-artifacts" / "targets" / "lot-a"
    receipt_path = target_dir / "receipt.json"
    receipt_path.unlink()

    result = runner.prepare(
        client, root, "prepare-tampered-artifacts",
        now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc),
    )
    assert not result["coverage_complete"]
    assert not result["successful_review_complete"]
    assert [(item["target_key"], item["status"]) for item in result["targets"]] == [("lot-a", "drafted")]
    assert result["targets"][0]["reason"] == "receipt_missing"

    assert runner.submit_targets(client, root, "prepare-tampered-artifacts", approved=True)["coverage_complete"]
    pending = json.loads((target_dir / "pending.json").read_text())
    pending["idempotency_key"] = "forged-key"
    runner.atomic_json(target_dir / "pending.json", pending)

    result = runner.prepare(
        client, root, "prepare-tampered-artifacts",
        now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc),
    )

    assert not result["coverage_complete"]
    assert not result["successful_review_complete"]
    assert [(item["target_key"], item["status"]) for item in result["targets"]] == [("lot-a", "failed")]
    assert result["targets"][0]["reason"] == "pending_invalid"


def test_existing_prepare_keeps_valid_receipt_without_research_or_model_work(tmp_path, monkeypatch):
    snap = snapshot(holding("lot-a", "instrument-a", "ALFA"))
    root = tmp_path / "root"
    client = FakeClient(snap)
    calls = []

    def counted_model(*args, **kwargs):
        calls.append(kwargs["target"]["holding_id"])
        return model_for_target(*args, **kwargs)

    _draft_one(root, "prepare-valid-receipt", client, model_fn=counted_model)
    monkeypatch.setattr(runner, "utc_now", lambda: datetime(2026, 1, 2, 12, tzinfo=timezone.utc))
    monkeypatch.setattr(runner, "now_text", lambda: NOW)
    assert runner.submit_targets(client, root, "prepare-valid-receipt", approved=True)["successful_review_complete"]
    reads = len(client.reads)

    result = runner.prepare(
        client, root, "prepare-valid-receipt",
        now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc),
    )

    assert result["coverage_complete"]
    assert result["successful_review_complete"]
    assert result["phase"] == "verified"
    assert calls == ["lot-a"]
    assert len(client.reads) == reads + 1


def test_existing_prepare_rejects_forged_receipt(tmp_path, monkeypatch):
    snap = snapshot(holding("lot-a", "instrument-a", "ALFA"))
    root = tmp_path / "root"
    client = FakeClient(snap)
    _draft_one(root, "prepare-forged-receipt", client)
    monkeypatch.setattr(runner, "utc_now", lambda: datetime(2026, 1, 2, 12, tzinfo=timezone.utc))
    monkeypatch.setattr(runner, "now_text", lambda: NOW)
    assert runner.submit_targets(client, root, "prepare-forged-receipt", approved=True)["coverage_complete"]
    receipt_path = root / "runs" / "prepare-forged-receipt" / "targets" / "lot-a" / "receipt.json"
    receipt = json.loads(receipt_path.read_text())
    receipt["target_key"] = "lot-forged"
    runner.atomic_json(receipt_path, receipt)

    result = runner.prepare(
        client, root, "prepare-forged-receipt",
        now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc),
    )

    assert not result["coverage_complete"]
    assert not result["successful_review_complete"]
    assert [(item["target_key"], item["status"]) for item in result["targets"]] == [("lot-a", "failed")]
    assert result["targets"][0]["reason"] == "receipt_invalid"


def test_existing_prepare_recomputes_unsupported_and_verified_completion(tmp_path, monkeypatch):
    snap = snapshot(
        holding("lot-a", "instrument-a", "ALFA"),
        holding("lot-b", "instrument-b", "OPTION", asset_type="option"),
    )
    root = tmp_path / "root"
    client = FakeClient(snap)
    _draft_one(root, "prepare-mixed-outcomes", client)
    monkeypatch.setattr(runner, "utc_now", lambda: datetime(2026, 1, 2, 12, tzinfo=timezone.utc))
    monkeypatch.setattr(runner, "now_text", lambda: NOW)
    submitted = runner.submit_targets(client, root, "prepare-mixed-outcomes", approved=True)
    assert submitted["coverage_complete"]
    assert not submitted["successful_review_complete"]

    manifest_path = root / "runs" / "prepare-mixed-outcomes" / "manifest.json"
    manifest = json.loads(manifest_path.read_text())
    manifest["successful_review_complete"] = True
    runner.atomic_json(manifest_path, manifest)

    result = runner.prepare(
        client, root, "prepare-mixed-outcomes",
        now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc),
    )

    assert result["coverage_complete"]
    assert not result["successful_review_complete"]
    assert [(item["target_key"], item["status"]) for item in result["targets"]] == [
        ("lot-a", "verified"), ("lot-b", "unsupported")
    ]
    assert result["targets"][0]["receipt_validated"] is True
    assert result["targets"][1]["reason"] == "unsupported_instrument_type"


class CommitThenRaiseClient(FakeClient):
    def __init__(self, snap, server=None):
        super().__init__(snap, server=server)
        self.raise_after_commit = True

    def request(self, method, path, payload=None, key=None):
        if method == "POST" and self.raise_after_commit:
            self.raise_after_commit = False
            target = payload["scope"]["holding_ids"][0]
            self.posts.append((target, key, payload))
            self.server[target] = (key, payload)
            raise ValueError("synthetic lost committed reply")
        return super().request(method, path, payload, key)


def test_prepare_preserves_exact_lost_reply_resume(tmp_path, monkeypatch):
    snap = snapshot(holding("lot-a", "instrument-a", "ALFA"))
    root = tmp_path / "root"
    first_client = FakeClient(snap)
    _draft_one(root, "prepare-lost-reply", first_client)
    monkeypatch.setattr(runner, "utc_now", lambda: datetime(2026, 1, 2, 12, tzinfo=timezone.utc))
    monkeypatch.setattr(runner, "now_text", lambda: NOW)
    pending_path = root / "runs" / "prepare-lost-reply" / "targets" / "lot-a" / "pending.json"
    pending = json.loads(pending_path.read_text())
    uncertain = CommitThenRaiseClient(snap)
    failed = runner.submit_targets(uncertain, root, "prepare-lost-reply", approved=True)
    assert not failed["coverage_complete"]

    resumed_client = FakeClient(snap, server=uncertain.server)
    prepared = runner.prepare(
        resumed_client, root, "prepare-lost-reply",
        now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc),
    )
    assert not prepared["coverage_complete"]
    resumed = runner.submit_targets(resumed_client, root, "prepare-lost-reply", approved=True)

    assert resumed["coverage_complete"]
    assert resumed_client.posts[0][1] == pending["idempotency_key"]
    assert resumed_client.posts[0][2] == pending["payload"]


def test_manifest_membership_must_match_current_snapshot_exactly():
    holdings = [holding("lot-a", "instrument-a", "ALFA"), holding("lot-b", "instrument-b", "BETA")]
    snap = snapshot(*holdings)
    manifest = {
        "snapshot_id": snap["snapshot_id"],
        "snapshot_sha256": runner.fingerprint(snap),
        "calculation_version": snap["calculation_version"],
        "targets": [
            {"target_key": h["holding_id"], "instrument_key": h["instrument_id"],
             "symbol": h["symbol"], "status": "verified", "review_id": "review-" + h["holding_id"]}
            for h in holdings
        ],
    }
    assert not runner._coverage_complete(manifest, snap)
    for targets in (
        manifest["targets"][:1],
        manifest["targets"] + [{"target_key": "extra", "instrument_key": "i-extra", "symbol": "EXTRA", "status": "verified"}],
        [manifest["targets"][0], manifest["targets"][0]],
    ):
        broken = dict(manifest, targets=targets)
        assert not runner._coverage_complete(broken, snap)
    changed = dict(snap, snapshot_id="snapshot-new")
    assert not runner._coverage_complete(manifest, changed)


def test_truncated_manifest_is_blocked_by_production_draft_path(tmp_path):
    holdings = [holding("lot-a", "instrument-a", "ALFA"), holding("lot-b", "instrument-b", "BETA")]
    snap = snapshot(*holdings)
    root = tmp_path / "root"
    client = FakeClient(snap)
    runner.prepare(client, root, "truncated", now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc))
    manifest_path = root / "runs" / "truncated" / "manifest.json"
    manifest = json.loads(manifest_path.read_text())
    manifest["targets"] = manifest["targets"][:1]
    runner.atomic_json(manifest_path, manifest)
    result = runner.draft_targets(
        root, "truncated", {"lot-a": evidence("a")}, config(), model_fn=model_for_target,
        generated_at=NOW, now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc),
    )
    assert not result["coverage_complete"]
    assert result["phase"] == "blocked"
    assert result["coverage_error"] == "manifest_snapshot_membership_mismatch"


def test_local_model_transport_refuses_remote_endpoint_without_fallback():
    with pytest.raises(ValueError, match="unapproved endpoint"):
        runner.local_http("GET", "https://remote.example/v1/models")


def test_unsupported_target_is_accounted_without_model_call(tmp_path):
    snap = snapshot(holding("lot-a", "instrument-a", "OPTION", asset_type="option"))
    root = tmp_path / "root"
    client = FakeClient(snap)

    def should_not_run(*args, **kwargs):
        raise AssertionError("unsupported target must not invoke model")

    result = runner.prepare(client, root, "unsupported-target", now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc))
    assert result["targets"][0]["status"] == "pending"
    result = runner.draft_targets(
        root, "unsupported-target", {"lot-a": evidence("a")}, config(),
        model_fn=should_not_run, generated_at=NOW,
        now=datetime(2026, 1, 2, 12, tzinfo=timezone.utc),
    )
    assert result["coverage_complete"]
    assert not result["successful_review_complete"]
    assert result["outcomes"] == [{"target_key": "lot-a", "status": "unsupported"}]


def test_progress_and_outcomes_are_public_status_only(tmp_path, monkeypatch):
    snap = snapshot(holding("lot-a", "instrument-a", "ALFA"))
    root = tmp_path / "root"
    client = FakeClient(snap)
    _draft_one(root, "public-progress", client)
    monkeypatch.setattr(runner, "utc_now", lambda: datetime(2026, 1, 2, 12, tzinfo=timezone.utc))
    monkeypatch.setattr(runner, "now_text", lambda: NOW)
    result = runner.submit_targets(client, root, "public-progress", approved=True)
    assert result["coverage_complete"]
    encoded = json.dumps(result) + (root / "runs" / "public-progress" / "manifest.json").read_text()
    for private_name in ("quantity", "unit_cost", "cost_basis", "latest_price", "latest_value", "reporting_value", "account", "weight"):
        assert private_name not in encoded

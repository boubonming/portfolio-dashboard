#!/usr/bin/env python3
"""Prevent shipped holding UI/docs from regressing to pre-mutation wording."""
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
TARGETS = [ROOT / "README.md", ROOT / "api" / "openapi.yaml", ROOT / "frontend" / "src" / "App.tsx"]
PROHIBITED = (
    "mutation handlers are not shipped",
    "does not expose state-changing portfolio http endpoints",
    "frontend holding controls remain explicitly pending",
    "holdings are shown from the approved import",
    "holding controls are pending",
)

for path in TARGETS:
    text = path.read_text(encoding="utf-8").lower()
    for phrase in PROHIBITED:
        if phrase in text:
            raise SystemExit(f"prohibited active holding wording: {path}:{phrase}")

print("holding mutation wording regression: PASS")

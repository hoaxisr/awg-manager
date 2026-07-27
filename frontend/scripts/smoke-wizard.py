#!/usr/bin/env python3
"""End-to-end smoke for the sing-box router setup wizard.

Run with mock-proxy already up (``npm run dev:mock:proxy``):

    uv run --with playwright python3 scripts/smoke-wizard.py

Walks the happy path: open page, click "Мастер", select 2 presets,
autopick tunnel, toggle-all devices, "Применить", wait for success.
"""

from __future__ import annotations
import os
import sys
from playwright.sync_api import sync_playwright

BASE = os.environ.get("BASE", "http://127.0.0.1:5173")
HEADLESS = os.environ.get("HEADLESS", "1") != "0"


def main() -> int:
    failed = False
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=HEADLESS)
        page = browser.new_page(viewport={"width": 1600, "height": 900})

        try:
            print("[smoke-wizard] navigate /sb/routing")
            page.goto(f"{BASE}/sb/routing")
            page.wait_for_load_state("networkidle")

            print("[smoke-wizard] click Мастер in header")
            wiz_btn = page.locator("button:visible").filter(has_text="Мастер").first
            wiz_btn.click(timeout=5000)
            page.wait_for_selector('[role="dialog"]', timeout=5000)

            print("[smoke-wizard] Step 1: select 2 presets")
            page.locator(".preset:visible").nth(0).click()
            page.locator(".preset:visible").nth(1).click()

            print("[smoke-wizard] Step 1: click Дальше")
            page.locator('button:visible:has-text("Дальше")').click()

            print("[smoke-wizard] Step 2: waiting for autopick to advance")
            page.wait_for_selector(
                '.title:has-text("Какие устройства")', timeout=10000
            )

            print("[smoke-wizard] Step 3: default-all on, click Дальше")
            page.locator('button:visible:has-text("Дальше")').click()

            print("[smoke-wizard] Step 4: summary, click Применить")
            page.wait_for_selector('.title:has-text("Что будет сделано")', timeout=5000)
            page.locator('button:visible:has-text("Применить")').click()

            print("[smoke-wizard] await success screen")
            page.wait_for_selector(
                'div:has-text("sing-box router запущен")', timeout=20000
            )

            print("[smoke-wizard] SMOKE OK")
            page.screenshot(path="/tmp/smoke-wizard-success.png", full_page=True)
        except Exception as e:
            failed = True
            print(f"[smoke-wizard] FAIL: {e}", file=sys.stderr)
            page.screenshot(path="/tmp/smoke-wizard-fail.png", full_page=True)

        browser.close()

    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())

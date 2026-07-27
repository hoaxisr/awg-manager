#!/usr/bin/env python3
"""Smoke test for the Modal.hasUnsavedChanges flow.

Run with mock-proxy already up (``npm run dev:mock:proxy`` in another terminal):
    python3 frontend/scripts/smoke-modal-unsaved.py

For each SCENARIO, opens the modal, types into a designated field, then
verifies:
  - clicking the backdrop pops the ConfirmModal (original modal stays)
  - clicking "Продолжить редактирование" closes the confirm, original stays
  - clicking backdrop again + "Закрыть без сохранения" closes both modals
  - reopening + immediate backdrop click (clean form) closes silently
"""

import sys
import time
from dataclasses import dataclass
from typing import Callable

from playwright.sync_api import sync_playwright, Page, TimeoutError as PWTimeout

BASE = "http://localhost:5173"


@dataclass
class Scenario:
    name: str
    navigate: Callable[[Page], None]
    trigger: Callable[[Page], None]
    input_selector: str
    typed_value: str = "x"
    make_dirty: Callable[[Page], None] | None = None  # Optional custom dirty-maker


def _open_edit_managed_server(p: Page):
    """Open EditManagedServerModal from /awg/servers via the cog icon on a server card."""
    # The server detail card shows a settings cog icon button with aria-label="Настройки"
    loc = p.locator('button[aria-label="Настройки"]').first
    if loc.count() > 0:
        loc.click(timeout=4000)
        return
    raise RuntimeError("could not find cog icon to open EditManagedServerModal")


SCENARIOS: list[Scenario] = [
    Scenario(
        name="EditManagedServerModal",
        navigate=lambda p: goto(p, "/awg/servers"),
        trigger=_open_edit_managed_server,
        # First text input in the open modal — usually the name (description) field
        input_selector='.modal-card input[type="text"]',
    ),
]

# Extend with three new modal scenarios
SCENARIOS.extend([
    Scenario(
        name="CreateManagedServerModal",
        navigate=lambda p: goto(p, "/awg/servers"),
        trigger=lambda p: p.locator('button:has-text("Новый сервер")').first.click(timeout=4000),
        input_selector='.modal-card input[type="text"]',
    ),
    Scenario(
        name="AddManagedPeerModal",
        navigate=lambda p: goto(p, "/awg/servers"),
        trigger=lambda p: p.locator('button:has-text("Добавить")').last.click(timeout=4000),
        input_selector='.modal-card input[type="text"]',
    ),
    Scenario(
        name="EditManagedPeerModal",
        navigate=lambda p: goto(p, "/awg/servers"),
        trigger=lambda p: p.locator('.peer-row').first.locator('.peer-action-btn').nth(1).click(timeout=4000),
        input_selector='#emp-desc',
    ),
])


def _open_dns_create_manual(p: Page):
    """DNS tab has '+ Добавить' dropdown -> 'Создать вручную' menu item."""
    p.locator('button:has-text("+ Добавить")').first.click(timeout=4000)
    p.wait_for_timeout(400)
    p.locator('.dropdown-item:has-text("Создать вручную")').first.click(timeout=4000)


SCENARIOS.extend([
    Scenario(
        name="DnsRouteEditModal_create",
        navigate=lambda p: goto(p, "/router/ndms"),
        trigger=_open_dns_create_manual,
        input_selector='.modal-card input[type="text"]',
    ),
    Scenario(
        name="IpRouteEditModal_create",
        navigate=lambda p: goto(p, "/router/ip"),
        trigger=lambda p: p.locator('button:has-text("+ Новое правило")').first.click(timeout=4000),
        input_selector='.modal-card input[type="text"]',
    ),
    Scenario(
        name="ClientRouteCreateModal",
        navigate=lambda p: goto(p, "/router/device-vpn"),
        trigger=lambda p: p.locator('button:has-text("+ Создать")').first.click(timeout=4000),
        input_selector='.device-row',  # Click first device to make form dirty
        typed_value='',  # Not used for button click
    ),
    Scenario(
        name="PolicyCreateModal",
        navigate=lambda p: goto(p, "/router/policies"),
        trigger=lambda p: p.locator('button:has-text("+ Создать")').first.click(timeout=4000),
        input_selector='.modal-card input[type="text"]',
    ),
    Scenario(
        name="HrNeoEditModal_create",
        navigate=lambda p: goto(p, "/services/hrneo"),
        trigger=lambda p: p.locator('button:has-text("+ Новое правило")').first.click(timeout=4000),
        input_selector='.modal-card input[type="text"]',
    ),
])


# Sing-box Router modals — Expert panel on /sb/routing?mode=expert.
def _nav_singbox_expert(p: Page):
    goto(p, "/sb/routing?mode=expert")


def _open_singbox_ruleset(p: Page):
    """Open RuleSetAddModal from Expert → Rule-sets."""
    p.locator('button:has-text("+ Набор")').first.click(timeout=4000)
    p.wait_for_timeout(600)


def _open_wizard_choose_step0(p: Page):
    """Open AddTunnelWizard with preselect='choose', starting at step 0 (clean state)."""
    # Click "+ Добавить" button on Sing-box tab (opens with preselect='choose')
    p.locator('button:has-text("+ Добавить")').first.click(timeout=4000)
    p.wait_for_timeout(500)


def _make_wizard_dirty(p: Page):
    """Make the wizard dirty by clicking a kind card to advance to step 1."""
    # Click the first kind card to advance to step 1
    p.locator('.kind-card').first.click(timeout=4000)
    p.wait_for_timeout(500)


SCENARIOS.extend([
    Scenario(
        name="AddTunnelWizard_choose",
        navigate=lambda p: goto(p, "/sb/tunnels"),
        trigger=_open_wizard_choose_step0,
        input_selector='',  # Not used since we have a custom make_dirty
        make_dirty=_make_wizard_dirty,
    ),
    Scenario(
        name="RuleEditModal_singbox",
        navigate=_nav_singbox_expert,
        trigger=lambda p: p.locator('button:has-text("+ Правило")').first.click(timeout=4000),
        input_selector='.modal-card textarea',
    ),
    Scenario(
        name="RuleSetAddModal",
        navigate=_nav_singbox_expert,
        trigger=_open_singbox_ruleset,
        input_selector='input[placeholder*="https://raw"]',
    ),
    Scenario(
        name="CompositeOutboundEditModal",
        navigate=_nav_singbox_expert,
        trigger=lambda p: p.locator('button:has-text("+ Outbound")').first.click(timeout=4000),
        input_selector='input[placeholder*="fast"]',
    ),
    Scenario(
        name="DNSServerEditModal",
        navigate=_nav_singbox_expert,
        trigger=lambda p: p.locator('button:has-text("+ Сервер")').first.click(timeout=4000),
        input_selector='input[placeholder*="bootstrap"]',
    ),
])


def goto(page: Page, path: str):
    page.goto(f"{BASE}{path}")
    page.wait_for_load_state("domcontentloaded")
    page.wait_for_timeout(1500)


def assert_modal_open(page: Page):
    page.locator(".modal-card").first.wait_for(state="visible", timeout=4000)


def assert_confirm_open(page: Page):
    page.locator('.modal-card:has-text("Несохранённые изменения")').wait_for(
        state="visible", timeout=3000
    )


def click_backdrop_corner(page: Page):
    page.mouse.move(10, 10)
    page.mouse.down()
    page.mouse.up()
    page.wait_for_timeout(400)


def run_scenario(page: Page, sc: Scenario) -> str:
    """Returns 'PASS' or a short error string."""
    try:
        sc.navigate(page)
        sc.trigger(page)
        assert_modal_open(page)

        # Type something to mark the form dirty.
        if sc.make_dirty:
            # Use custom dirty-maker if provided
            sc.make_dirty(page)
        else:
            # For .device-row or button selectors, use click(); for text inputs, use fill().
            elem = page.locator(sc.input_selector).first
            if 'device-row' in sc.input_selector or 'button' in sc.input_selector:
                elem.click(timeout=3000)
            else:
                elem.fill(sc.typed_value, timeout=3000)
        page.wait_for_timeout(200)

        # Backdrop while dirty -> ConfirmModal appears.
        click_backdrop_corner(page)
        assert_confirm_open(page)

        # Cancel from confirm -> confirm closes, parent stays.
        page.locator('button:has-text("Продолжить редактирование")').first.click(timeout=2000)
        try:
            page.locator('.modal-card:has-text("Несохранённые изменения")').first.wait_for(state="detached", timeout=2000)
        except PWTimeout:
            return "confirm did not close on cancel"
        assert_modal_open(page)

        # Backdrop again + confirm-destroy.
        click_backdrop_corner(page)
        assert_confirm_open(page)
        page.locator('button:has-text("Закрыть без сохранения")').first.click(timeout=2000)
        try:
            page.locator('.modal-card').first.wait_for(state="detached", timeout=2000)
        except PWTimeout:
            return "parent did not close on destroy"

        # Reopen clean -> backdrop closes silently.
        sc.trigger(page)
        assert_modal_open(page)
        click_backdrop_corner(page)
        try:
            page.locator('.modal-card').first.wait_for(state="detached", timeout=2000)
        except PWTimeout:
            return "clean form did not close silently"

        return "PASS"
    except PWTimeout as e:
        return f"timeout: {str(e)[:120]}"
    except Exception as e:
        return f"{type(e).__name__}: {str(e)[:120]}"


def main():
    if not SCENARIOS:
        print("No SCENARIOS defined yet; nothing to run.")
        return 0
    with sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True)
        ctx = browser.new_context(viewport={"width": 1600, "height": 1000})
        page = ctx.new_page()
        fails = 0
        for sc in SCENARIOS:
            res = run_scenario(page, sc)
            mark = "PASS" if res == "PASS" else "FAIL"
            if res != "PASS":
                fails += 1
            print(f"{mark}  {sc.name:40s} {res if res != 'PASS' else ''}")
        browser.close()
        return 0 if fails == 0 else 1


if __name__ == "__main__":
    sys.exit(main())

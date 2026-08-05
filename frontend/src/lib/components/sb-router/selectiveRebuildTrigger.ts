/**
 * Helper for triggering a selective-bypass ipset rebuild after rule mutations.
 * Called from AddWizardPanel, RulesPanel and any other place that modifies
 * router rules or rule sets while selective mode may be active.
 *
 * The rebuild runs in the background: progress arrives via SSE
 * (singbox-router:selective-progress) → selectiveBypass store → the
 * SelectiveRebuildModal mounted in the sing-box engine group layout
 * (routes/sb/(engine)/+layout.svelte) opens on requestModal() below.
 */
import { get } from 'svelte/store';
import { api } from '$lib/api/client';
import { singboxRouter } from '$lib/stores/singboxRouter';
import { selectiveBypass } from '$lib/stores/selectiveBypass';

/**
 * Trigger an ipset rebuild if the router settings have selectiveBypass enabled.
 * The POST is async: the backend answers 202 immediately (status with
 * rebuilding: true) and completion arrives via SSE. Errors — including gateway
 * timeouts (ApiGatewayError), after which the rebuild keeps running server-side
 * — are silently swallowed: a stale ipset is preferable to surfacing an extra
 * error notification after a successful rule save.
 */
export async function triggerSelectiveRebuildIfEnabled(): Promise<void> {
  // ЗАВИСИМОСТЬ ОТ ПРАЙМИНГА: незагруженные настройки неотличимы здесь от
  // выключенного selectiveBypass — обе ветки молча выходят. Пока баннер жил на
  // странице движка, настройки к этому моменту всегда были подняты её loadAll;
  // теперь баннер общий, и «Применить» жмут в том числе со страниц, которые
  // ничего не грузят. Настройки для них праймит routes/sb/+layout.svelte
  // (reloadSettings) — уберёшь его, и пересборка ipset перестанет запускаться.
  const settings = get(singboxRouter.settings);
  if (!settings?.selectiveBypass) return;
  selectiveBypass.resetProgress();
  selectiveBypass.requestModal();
  try {
    const status = await api.singboxRouterSelectiveRebuild();
    selectiveBypass.applyStatus(status);
  } catch {
    // non-fatal — progress/error arrives via SSE anyway
  }
}

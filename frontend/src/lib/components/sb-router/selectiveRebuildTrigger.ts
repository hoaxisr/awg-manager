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
import { api, ApiGatewayError } from '$lib/api/client';
import { notifications } from '$lib/stores/notifications';
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

/** Чем кончился ручной запуск пересборки. Возвращается ради тестов и логов —
 *  пользователю о неудаче рассказывает тост изнутри. */
export type SelectiveRebuildOutcome =
  /** 202 принят, статус применён — пересборка идёт, прогресс доедет по SSE. */
  | 'started'
  /** 202 принят, но статус НЕ применён: пока ждали ответ, прилетел более свежий. */
  | 'superseded'
  /** Шлюз не дождался ответа; пересборка продолжается на роутере. */
  | 'background'
  /** 409: сейчас применяется конфигурация sing-box. */
  | 'busy'
  | 'failed';

/**
 * Ручная пересборка ipset — кнопка «Пересобрать ipset» на странице «Движок».
 *
 * ЧЕМ ОТЛИЧАЕТСЯ ОТ `triggerSelectiveRebuildIfEnabled` выше и почему нельзя
 * звать его вместо этого. Тот — молчаливый пост-Apply вариант: он сам решает,
 * надо ли пересобирать (по настройкам), и ГЛОТАЕТ любые ошибки, потому что
 * тост сразу после успешного сохранения правил только путает. Кнопку нажал
 * человек — он обязан узнать, что пересборка не пошла. Плюс здесь есть две
 * вещи, которых там нет:
 *
 *  * epoch-guard. 202 значит «запущено», а не «завершено». Мгновенно упавшая
 *    пересборка публикует терминальный SSE selective-status РАНЬШЕ, чем
 *    разрешится 202; применив тело ответа поверх, мы вернули бы rebuilding:
 *    true, и кнопка залипла бы в «Пересборка…» до перезагрузки страницы.
 *  * ApiGatewayError без тоста. Nginx не дождался ответа, но пересборка идёт на
 *    роутере и доедет по SSE — ошибки для пользователя тут нет.
 *
 * Настройки не проверяем: кнопка и так видна только при включённом селективном
 * перехвате, а «нажал и ничего не произошло» — худший из возможных ответов.
 *
 * Окно прогресса рисует SelectiveRebuildModal из layout группы — отсюда его
 * только просят открыться (requestModal).
 */
export async function triggerSelectiveRebuildManual(): Promise<SelectiveRebuildOutcome> {
  selectiveBypass.resetProgress();
  selectiveBypass.requestModal();
  const epochBefore = selectiveBypass.statusEpoch();
  try {
    const status = await api.singboxRouterSelectiveRebuild();
    if (selectiveBypass.statusEpoch() !== epochBefore) return 'superseded';
    selectiveBypass.applyStatus(status);
    return 'started';
  } catch (e) {
    if (e instanceof ApiGatewayError) {
      console.warn('selective rebuild: gateway error, rebuild continues in background', e);
      return 'background';
    }
    if ((e as { body?: { code?: string } })?.body?.code === 'OPERATION_IN_PROGRESS') {
      notifications.error(
        'Пересборка недоступна: применяется конфигурация sing-box. Повторите позже.',
      );
      return 'busy';
    }
    notifications.error(
      'Не удалось пересобрать ipset: ' + (e instanceof Error ? e.message : String(e)),
    );
    return 'failed';
  }
}

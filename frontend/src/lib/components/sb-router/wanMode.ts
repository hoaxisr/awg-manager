import type { SingboxRouterSettings } from '$lib/types';

// Локальный оверрайд режима WAN. Выключение авто-определения само по себе
// невалидно для бэкенда (нужен выбранный интерфейс), поэтому показываем
// селектор через оверрайд, а персистим только после выбора интерфейса.
// true никогда не ставится — возврат к авто идёт через persist + null.
export type WanAutoOverride = false | null;

export interface WanModeAction {
  /** Новое значение локального оверрайда. */
  override: WanAutoOverride;
  /** Что персистить; null — не сохранять. */
  patch: Partial<SingboxRouterSettings> | null;
}

/** Видимый режим: оверрайд приоритетнее persisted-настройки. */
export function resolveWanAuto(
  override: WanAutoOverride,
  persisted: boolean | undefined,
): boolean {
  return override ?? persisted ?? true;
}

/** Тоггл «Авто-определение». Выключение не персистит — пустой wanInterface бэкенд отклонит. */
export function planToggleAutoDetect(checked: boolean): WanModeAction {
  return checked
    ? { override: null, patch: { wanAutoDetect: true, wanInterface: '' } }
    : { override: false, patch: null };
}

/** Выбор интерфейса. Пустое значение («— выберите —») — no-op. */
export function planSelectWanInterface(value: string): WanModeAction | null {
  if (!value) return null;
  return { override: null, patch: { wanAutoDetect: false, wanInterface: value } };
}

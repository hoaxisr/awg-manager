// Сохранение настроек движка со страницы «Движок».
//
// Пайплайн общий с sb-router (`mergeAndSaveSettings`: свежий GET → merge → PUT
// → loadAll) — PUT уносит полный объект, и эхо устаревшего стора молча
// откатывало бы чужие правки. Здесь обёртка с тостом об ошибке (чтобы карточки
// страницы не разъезжались в формулировках) и общий признак «идёт сохранение».
//
// ЗАЧЕМ ПРИЗНАК. На странице больше десятка полей с автосохранением и ни одной
// кнопки «Сохранить»: тумблер sniff, UDP-таймаут, WAN, пресеты исключений,
// порты, подсети, пулы fakeip, MTU, стек, источник трафика, классы QoS. Без
// индикатора пользователь не знает, доехала ли правка до роутера (E08
// инвентаря волны, решение §3 п.4 — «остаётся, место — шапка страницы»).
// Тост об ошибке при этом сохраняется: он единственное место, где виден ТЕКСТ
// ошибки.
import { writable } from 'svelte/store';
import { mergeAndSaveSettings } from '$lib/components/sb-router/settingsActions';
import { notifications } from '$lib/stores/notifications';
import type { SingboxRouterSettings } from '$lib/types';

/** `idle` — на этой странице ещё ничего не сохраняли, показывать нечего. */
export type EngineSaveState = 'idle' | 'saving' | 'saved' | 'error';

const state = writable<EngineSaveState>('idle');

/** Состояние индикатора автосохранения страницы «Движок». */
export const engineSaveState = { subscribe: state.subscribe };

// Сохранения идут параллельно (очередь QoS, blur одного поля во время правки
// другого): «сохранено» обязано наступить, когда закончилось ПОСЛЕДНЕЕ, иначе
// быстрый успех гасит индикатор ещё идущего сохранения. Ошибка внутри пачки
// липкая — иначе успех соседнего поля стёр бы единственный признак провала.
let inFlight = 0;
let batchFailed = false;

/** Только для тестов: вернуть индикатор в исходное состояние. */
export function resetEngineSaveState(): void {
	inFlight = 0;
	batchFailed = false;
	state.set('idle');
}

export async function applyEngineSettings(
	patch: Partial<SingboxRouterSettings>,
): Promise<boolean> {
	if (inFlight === 0) batchFailed = false;
	inFlight += 1;
	state.set('saving');
	try {
		await mergeAndSaveSettings(patch);
		return true;
	} catch (e) {
		batchFailed = true;
		notifications.error(`Не удалось сохранить: ${e instanceof Error ? e.message : String(e)}`);
		return false;
	} finally {
		inFlight -= 1;
		if (inFlight <= 0) {
			inFlight = 0;
			state.set(batchFailed ? 'error' : 'saved');
		}
	}
}

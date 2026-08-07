// Чипы слотов config.d для карточки «Конфигурация».
//
// ИМЕНА СЛОТОВ — ТОЛЬКО ИЗ API. Раскладка каталога менялась в 5D0
// (20-tproxy/20-policytun/20-fakeip + общий 21-routing вместо 05/06/07/20), и
// свой список на фронте разъехался бы с бэкендом на следующей же правке
// orchestrator.KnownSlots(). Здесь только разбор ответа.
//
// ПОКАЗЫВАЕМ НЕ ВСЕ. `GET /singbox/config/slots` отдаёт ВЕСЬ закрытый набор
// слотов, включая несконфигурированные (`size: 0` — файла нет ни в config.d, ни
// в disabled/). Такой чип соврал бы: он обещает фрагмент конфига, которого не
// существует. Взаимоисключающие режимные слоты — ровно тот случай: в tproxy
// чипы 20-fakeip и 20-policytun означали бы, что режим собран трижды.
import type { ConfigSlotInfo } from '$lib/types';

/** Состояние слота, влияющее на вид чипа. */
export type SlotChipState = 'active' | 'draft' | 'off';

export interface SlotChip {
	slot: string;
	/** Имя файла без расширения: `20-tproxy`. */
	label: string;
	/** Приписка к имени: «выкл», «черновик» или обе. Пусто у обычного слота. */
	note: string;
	state: SlotChipState;
	/** Подсказка: владелец слота и что с ним не так. */
	title: string;
}

export function slotChips(slots: readonly ConfigSlotInfo[] | null | undefined): SlotChip[] {
	if (!slots) return [];
	// Порядок ответа — порядок слияния файлов, и это единственный порядок,
	// который что-то значит: чипы читаются как «во что собирается конфиг».
	return slots.filter((s) => s.size > 0).map(toChip);
}

function toChip(s: ConfigSlotInfo): SlotChip {
	const notes: string[] = [];
	if (!s.enabled) notes.push('выкл');
	if (s.hasDraft) notes.push('черновик');

	// «Выключен» важнее черновика: выключенный слот не участвует в сборке
	// вообще, и его черновик — вопрос второго порядка.
	const state: SlotChipState = !s.enabled ? 'off' : s.hasDraft ? 'draft' : 'active';

	const parts = [
		s.ownership === 'user' ? 'пользовательский слот' : 'генерируется автоматически',
	];
	if (!s.enabled) parts.push('в сборку не входит');
	if (s.hasDraft) parts.push('есть непринятый черновик');

	return {
		slot: s.slot,
		label: s.filename.replace(/\.json$/i, ''),
		note: notes.join(' · '),
		state,
		title: `${s.filename} — ${parts.join(', ')}`,
	};
}

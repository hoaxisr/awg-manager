// Состояние посева прокси-подсистемы словами пользователя.

import type {
	ProxyListenMoveView,
	ProxySeedView,
	ProxySkippedSourceView,
} from '$lib/api/proxyInstances';

const GATE_LOCKED =
	'Посев прокси-подсистемы не подтверждён: уборка осиротевших интерфейсов и маршрутов заблокирована.';

/**
 * Пропущенный посевом старый конфиг словами пользователя: его инстансы не
 * перенеслись и не появятся — повторов посева нет, файл никто не чинит.
 */
function skippedText(s: ProxySkippedSourceView): string {
	const head = `старый конфиг ${s.file} не разобран, его инстансы не перенесены`;
	const why = s.reason?.trim();
	return why ? `${head}: ${why}` : head;
}

/**
 * Текст предупреждения о запертом гейте посева — или пусто, когда говорить не
 * о чем.
 *
 * Признака у посева ДВА, и слить их нельзя: `seeded` — подсистема поднялась,
 * `certified` — посев подтверждён реестру и уборка разрешена. Состояние
 * «поднялась, но не подтверждена» инстансы показывает как обычно, и без этой
 * строки запертый гейт не виден вообще ниоткуда.
 *
 * Несостоявшийся посев (`!seeded`) сюда не доходит и молчит намеренно: список
 * инстансов при нём пуст по построению, клиент отвечает отказом с причиной, и
 * страница показывает её вместо содержимого — второй раз про то же не говорим.
 *
 * Пропущенные источники вытесняют `error`, а не дописываются к нему: причина
 * запертого гейта там та же самая, и пользователь прочитал бы её дважды.
 */
export function seedGateWarning(seed: ProxySeedView | null | undefined): string {
	if (!seed || !seed.seeded || seed.certified) return '';
	const skipped = seed.skipped ?? [];
	if (skipped.length) return `${GATE_LOCKED} ${skipped.map(skippedText).join('; ')}`;
	const why = seed.error?.trim();
	return why ? `${GATE_LOCKED} ${why}` : GATE_LOCKED;
}

/**
 * Сообщение о переезде listen-порта — или пусто, когда никто не переезжал.
 *
 * Отдельно от `seedGateWarning`, а не внутри него: тот говорит только при
 * запертом гейте (`seeded && !certified`), а переезд случается и на нормально
 * заверенном посеве — там сообщение просто не появилось бы. Причина сказать
 * есть всегда: снаружи мог быть настроен клиент на прежний порт, и после
 * обновления он молча перестал бы соединяться.
 */
export function seedListenMoveNotice(seed: ProxySeedView | null | undefined): string {
	const moved = seed?.movedListen ?? [];
	if (!moved.length) return '';
	return `Дефолтный порт у прокси-подсистем совпадал, поэтому при переносе настроек он остался за одним инстансом: ${moved
		.map(moveText)
		.join('; ')}. Если снаружи настроено подключение на прежний адрес, поправьте его.`;
}

function moveText(m: ProxyListenMoveView): string {
	const who = m.name?.trim() || m.instance;
	return `«${who}» переехал с ${m.from} на ${m.to}`;
}

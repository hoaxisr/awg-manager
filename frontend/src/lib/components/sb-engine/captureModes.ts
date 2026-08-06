// Тексты карточки «Захват трафика» и пикера режима.
//
// ПОЧЕМУ ОТДЕЛЬНЫЙ МОДУЛЬ, А НЕ СТРОКИ В РАЗМЕТКЕ. Одно и то же название режима
// живёт уже в трёх местах (бейдж сайдбара, пилюля шапки, экран подтверждения) —
// все они читают `humanLabel` из fakeip/switchConsequences. Карточка добавляет к
// названию ещё две строки (объяснение и техническую подпись), и если держать их
// в разметке, проверить «в пикере ровно три режима и у каждого своё описание»
// можно будет только рендером.
//
// Список режимов ЗДЕСЬ — единственный источник для пикера: `modeSwitch.request`
// принимает и 'off' (выключение движка), но выключение — это кнопка в шапке, а
// не четвёртая карточка выбора.
import { humanLabel } from '$lib/components/fakeip/switchConsequences';
import type { EngineRoutingMode } from './engineRunState';

export interface CaptureModeCopy {
	mode: EngineRoutingMode;
	/** Название режима — общее с бейджем сайдбара и экраном подтверждения. */
	label: string;
	/** Однострочное объяснение: что режим делает с точки зрения пользователя. */
	summary: string;
	/** Техническая подпись: чем режим поднимается на роутере. */
	tech: string;
}

/** Порядок пикера: от самого простого в настройке к самому требовательному. */
export const CAPTURE_MODES: readonly EngineRoutingMode[] = ['tproxy', 'policy-tun', 'fakeip-tun'];

// Технические подписи намеренно совпадают по составу с режимными подсказками
// сбоя (`engineRunState.FAILURE_HINTS`): подпись говорит, ЧТО поднимается,
// подсказка — что из этого не поднялось. Расхождение между ними читалось бы как
// два разных описания одного режима.
const SUMMARY: Record<EngineRoutingMode, string> = {
	tproxy: 'Роутер перехватывает трафик правилами iptables — на устройствах настраивать нечего.',
	'policy-tun':
		'Трафик заходит в туннель у тех устройств, которым интерфейс разрешён политикой доступа Keenetic.',
	'fakeip-tun':
		'Домены получают адреса из fakeip-пула; DNS туннеля прописывается на клиентах вручную.',
};

const TECH: Record<EngineRoutingMode, string> = {
	tproxy: 'iptables TPROXY: цепочки AWGM + jump в PREROUTING',
	'policy-tun': 'tun-инбаунд + дефолт-маршрут NDMS на интерфейсе',
	'fakeip-tun': 'tun-инбаунд + hijack-dns + NDMS-маршруты на fakeip-пул',
};

export function captureModeCopy(mode: EngineRoutingMode): CaptureModeCopy {
	return { mode, label: humanLabel(mode), summary: SUMMARY[mode], tech: TECH[mode] };
}

/** Все три варианта пикера в порядке показа. */
export function captureModeOptions(): CaptureModeCopy[] {
	return CAPTURE_MODES.map(captureModeCopy);
}

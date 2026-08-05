import { describe, it, expect } from 'vitest';
import { NAV_TREE, activeItem, breadcrumbFor, groupItems, isSeparator } from './navigation';
import type { NavGroup } from './navigation';

const u = (path: string) => new URL(`http://router${path}`);

/** Все кликабельные пункты дерева: пункты групп без разделителей + плоские. */
const allItems = () => NAV_TREE.flatMap((e) => (e.kind === 'group' ? groupItems(e) : [e]));

const sbGroup = (): NavGroup => {
	const entry = NAV_TREE.find((e) => e.id === 'sb');
	if (!entry || entry.kind !== 'group') throw new Error('в дереве нет группы sb');
	return entry;
};

describe('NAV_TREE hrefs', () => {
	// Контейнеров-с-вкладками в дереве больше нет: каждый пункт — свой путь.
	// `?tab=` в href означал бы, что раздел снова живёт вкладкой чужой страницы.
	it('ни один пункт не ссылается на вкладку', () => {
		for (const item of allItems()) {
			expect(item.href).not.toContain('?tab=');
		}
	});
});

describe('группа Sing-box', () => {
	// Состав и порядок — таблица §2 спеки 5D. Тринадцать пунктов: «14» в
	// заголовке таблицы — хвост редакции 1, где ещё был раздел «Устройства».
	it('тринадцать пунктов в порядке спеки', () => {
		expect(groupItems(sbGroup()).map((i) => [i.id, i.label, i.href])).toEqual([
			['sb-engine', 'Движок', '/sb/engine'],
			['sb-tunnels', 'Туннели', '/sb/tunnels'],
			['sb-awg3', 'Endpoints', '/sb/awg3'],
			['sb-subs', 'Подписки', '/sb/subscriptions'],
			['sb-groups', 'Группы', '/sb/groups'],
			['sb-wizard', 'Мастер', '/sb/wizard'],
			['sb-rules', 'Маршруты', '/sb/rules'],
			['sb-rule-sets', 'Rule sets', '/sb/rule-sets'],
			['sb-dns', 'DNS', '/sb/dns'],
			['sb-inbounds', 'Inbounds', '/sb/inbounds'],
			['sb-connections', 'Соединения движка', '/sb/connections'],
			['sb-logs', 'Журнал движка', '/sb/logs'],
			['sb-geodata', 'Гео-данные', '/sb/geodata'],
		]);
	});

	// Находка ревью 5A: в свёрнутой группе голые «Соединения» и «Журнал»
	// неотличимы от одноимённых пунктов верхнего уровня.
	it('ни один пункт не тёзка верхнеуровневого', () => {
		const topLabels = NAV_TREE.filter((e) => e.kind === 'link').map((e) => e.label);
		for (const item of groupItems(sbGroup())) {
			expect(topLabels).not.toContain(item.label);
		}
	});

	it('три разделителя в порядке спеки', () => {
		expect(sbGroup().items.filter(isSeparator).map((s) => s.label)).toEqual([
			'outbounds',
			'правила',
			'наблюдение',
		]);
	});

	it('разделители стоят перед своими пунктами', () => {
		const ids = sbGroup().items.map((e) => e.id);
		expect(ids.indexOf('sb-sep-outbounds')).toBe(ids.indexOf('sb-tunnels') - 1);
		expect(ids.indexOf('sb-sep-rules')).toBe(ids.indexOf('sb-wizard') - 1);
		expect(ids.indexOf('sb-sep-watch')).toBe(ids.indexOf('sb-inbounds') - 1);
	});

	// Разделитель не может стать активным не потому, что его матчер возвращает
	// false, а потому, что матчера у него нет вовсе — как и адреса.
	it('у разделителя нет ни адреса, ни матчера', () => {
		const separators = sbGroup().items.filter(isSeparator);
		expect(separators.length).toBeGreaterThan(0);
		for (const sep of separators) {
			expect('href' in sep).toBe(false);
			expect('match' in sep).toBe(false);
		}
	});

	// Разделитель — типографская группировка, а не уровень вложенности.
	it('третьего уровня в группе нет', () => {
		for (const entry of sbGroup().items) {
			expect('items' in entry).toBe(false);
		}
	});

	// Модель статическая: она объявляет ИСТОЧНИК бейджа, значение приезжает из
	// сторов (см. stores/navBadges.ts).
	it('бейджи объявлены источником, а не значением', () => {
		const declared = groupItems(sbGroup())
			.filter((i) => i.badge !== undefined)
			.map((i) => [i.id, i.badge]);
		expect(declared).toEqual([
			['sb-engine', 'mode'],
			['sb-groups', 'groups'],
			['sb-rules', 'rules'],
			['sb-rule-sets', 'rule-sets'],
			['sb-connections', 'connections'],
		]);
	});

	// В Status DTO движка счётчика DNS нет — источник без данных не заводим.
	it('у DNS бейджа нет', () => {
		expect(groupItems(sbGroup()).find((i) => i.id === 'sb-dns')?.badge).toBeUndefined();
	});
});

describe('activeItem', () => {
	it('«Все туннели» на /tunnels', () => {
		expect(activeItem(u('/tunnels'))?.item.id).toBe('tunnels-all');
		expect(activeItem(u('/tunnels?detail=t1'))?.item.id).toBe('tunnels-all');
	});
	it('«Обзор» — первый пункт дерева, «Все туннели» — второй', () => {
		expect(NAV_TREE[0].id).toBe('overview');
		expect(NAV_TREE[1].id).toBe('tunnels-all');
	});
	it('корень → «Обзор»', () => {
		expect(activeItem(u('/'))?.item.id).toBe('overview');
		expect(activeItem(u('/?tab=awg'))?.item.id).toBe('overview');
	});
	it('«Обзор» матчится точным путём, а не префиксом', () => {
		// href '/' с префиксным матчером подсветил бы вообще всё дерево.
		expect(activeItem(u('/settings'))?.item.id).toBe('settings');
		expect(activeItem(u('/tunnels'))?.item.id).toBe('tunnels-all');
		expect(activeItem(u('/nope'))).toBeNull();
	});
	it('AWG-туннели на своём маршруте', () => {
		expect(activeItem(u('/awg/tunnels'))?.item.id).toBe('awg-tunnels');
		expect(activeItem(u('/awg/tunnels?detail=t1'))?.item.id).toBe('awg-tunnels');
	});
	it('детальные страницы туннелей → AWG Туннели', () => {
		expect(activeItem(u('/awg/tunnels/abc'))?.item.id).toBe('awg-tunnels');
		expect(activeItem(u('/awg/tunnels/new'))?.item.id).toBe('awg-tunnels');
		expect(activeItem(u('/awg/tunnels/system/nwg0'))?.item.id).toBe('awg-tunnels');
	});
	it('главная больше не подсвечивает AWG-туннели', () => {
		expect(activeItem(u('/'))?.item.id).not.toBe('awg-tunnels');
		expect(activeItem(u('/?tab=awg'))?.item.id).not.toBe('awg-tunnels');
	});
	it('sing-box туннели на своём маршруте', () => {
		expect(activeItem(u('/sb/tunnels'))?.item.id).toBe('sb-tunnels');
		expect(activeItem(u('/sb/tunnels/tag-1'))?.item.id).toBe('sb-tunnels');
		expect(activeItem(u('/sb/tunnels/new'))?.item.id).toBe('sb-tunnels');
	});
	it('AWG3 на своём маршруте', () => {
		expect(activeItem(u('/sb/awg3'))?.item.id).toBe('sb-awg3');
	});
	it('подписки на своём маршруте', () => {
		expect(activeItem(u('/sb/subscriptions'))?.item.id).toBe('sb-subs');
	});
	it('главная больше не подсвечивает sing-box туннели, AWG3 и подписки', () => {
		expect(activeItem(u('/?tab=singbox'))?.item.id).not.toBe('sb-tunnels');
		expect(activeItem(u('/?tab=awg3'))?.item.id).not.toBe('sb-awg3');
		expect(activeItem(u('/?tab=subscriptions'))?.item.id).not.toBe('sb-subs');
	});
	it('сервисы на своих маршрутах', () => {
		expect(activeItem(u('/services/freeturn'))?.item.id).toBe('svc-freeturn');
		expect(activeItem(u('/services/wdtt'))?.item.id).toBe('svc-wdtt');
	});
	it('главная больше не подсвечивает FreeTurn/WDTT', () => {
		expect(activeItem(u('/?tab=freeturn'))?.item.id).not.toBe('svc-freeturn');
		expect(activeItem(u('/?tab=wdtt'))?.item.id).not.toBe('svc-wdtt');
	});
	it('детальные sb-страницы → свои пункты', () => {
		expect(activeItem(u('/sb/subscriptions/5'))?.item.id).toBe('sb-subs');
	});
	it('разделы роутера на своих маршрутах', () => {
		expect(activeItem(u('/router/ndms'))?.item.id).toBe('router-ndms');
		expect(activeItem(u('/router/ip'))?.item.id).toBe('router-ip');
		expect(activeItem(u('/router/device-vpn'))?.item.id).toBe('router-device-vpn');
		expect(activeItem(u('/router/policies'))?.item.id).toBe('router-policies');
		// ?edit= из поиска — параметр поверхности, на подсветку не влияет.
		expect(activeItem(u('/router/ndms?edit=r1'))?.item.id).toBe('router-ndms');
		expect(activeItem(u('/router/ip?edit=r2'))?.item.id).toBe('router-ip');
	});
	it('HR Neo на своём маршруте', () => {
		expect(activeItem(u('/services/hrneo'))?.item.id).toBe('svc-hrneo');
		expect(activeItem(u('/services/hrneo?edit=r3'))?.item.id).toBe('svc-hrneo');
	});
	it('контейнер /routing мёртв — ничего не подсвечивает', () => {
		expect(activeItem(u('/routing'))).toBeNull();
		expect(activeItem(u('/routing?tab=dns'))).toBeNull();
		expect(activeItem(u('/routing?tab=ip'))).toBeNull();
		expect(activeItem(u('/routing?tab=clientvpn'))).toBeNull();
		expect(activeItem(u('/routing?tab=policy'))).toBeNull();
		expect(activeItem(u('/routing?tab=hrneo'))).toBeNull();
	});
	it('страницы группы движка на своих маршрутах', () => {
		expect(activeItem(u('/sb/engine'))?.item.id).toBe('sb-engine');
		expect(activeItem(u('/sb/groups'))?.item.id).toBe('sb-groups');
		expect(activeItem(u('/sb/wizard'))?.item.id).toBe('sb-wizard');
		expect(activeItem(u('/sb/rules'))?.item.id).toBe('sb-rules');
		expect(activeItem(u('/sb/rule-sets'))?.item.id).toBe('sb-rule-sets');
		expect(activeItem(u('/sb/dns'))?.item.id).toBe('sb-dns');
		expect(activeItem(u('/sb/inbounds'))?.item.id).toBe('sb-inbounds');
		expect(activeItem(u('/sb/connections'))?.item.id).toBe('sb-connections');
		expect(activeItem(u('/sb/logs'))?.item.id).toBe('sb-logs');
	});
	it('соседние маршруты /sb/rules и /sb/rule-sets не путаются', () => {
		expect(activeItem(u('/sb/rule-sets'))?.item.id).not.toBe('sb-rules');
		expect(activeItem(u('/sb/rules'))?.item.id).not.toBe('sb-rule-sets');
	});
	it('соединения и журнал движка не перебивают верхнеуровневые', () => {
		expect(activeItem(u('/connections'))?.item.id).toBe('connections');
		expect(activeItem(u('/connections'))?.group).toBeNull();
		expect(activeItem(u('/logs'))?.item.id).toBe('logs');
		expect(activeItem(u('/logs'))?.group).toBeNull();
		expect(activeItem(u('/sb/connections'))?.group?.id).toBe('sb');
		expect(activeItem(u('/sb/logs'))?.group?.id).toBe('sb');
	});
	it('пункта «Маршрутизация» в дереве больше нет — страница разобрана', () => {
		// /sb/routing раздаёт легаси-закладки редиректом (routes/sb/routing/+page.ts),
		// пунктом меню он быть перестал.
		expect(allItems().map((i) => i.id)).not.toContain('sb-routing');
		expect(allItems().map((i) => i.href)).not.toContain('/sb/routing');
		expect(activeItem(u('/sb/routing'))).toBeNull();
	});
	it('гео-данные на своём маршруте', () => {
		expect(activeItem(u('/sb/geodata'))?.item.id).toBe('sb-geodata');
	});
	it('серверы, журнал и настройки', () => {
		expect(activeItem(u('/awg/servers'))?.item.id).toBe('awg-servers');
		expect(activeItem(u('/awg/servers/managed-asc?id=x'))?.item.id).toBe('awg-servers');
		expect(activeItem(u('/logs'))?.item.id).toBe('logs');
		expect(activeItem(u('/settings'))?.item.id).toBe('settings');
	});
	it('оперативные поверхности — пункты верхнего уровня', () => {
		expect(activeItem(u('/logs'))?.item.id).toBe('logs');
		expect(activeItem(u('/monitoring'))?.item.id).toBe('monitoring');
		expect(activeItem(u('/connections'))?.item.id).toBe('connections');
		expect(activeItem(u('/diagnostics'))?.item.id).toBe('diagnostics');
	});
	it('вкладки диагностики не меняют подсветку раздела', () => {
		expect(activeItem(u('/diagnostics?tab=about'))?.item.id).toBe('diagnostics');
		expect(activeItem(u('/diagnostics?tab=dns'))?.item.id).toBe('diagnostics');
	});
	it('раздела «Инструменты» в дереве больше нет', () => {
		expect(allItems().map((i) => i.id)).not.toContain('tools');
		expect(activeItem(u('/tools'))).toBeNull();
	});
	it('AWG3-раздел называется Endpoints', () => {
		expect(groupItems(sbGroup()).find((i) => i.id === 'sb-awg3')?.label).toBe('Endpoints');
	});
	it('неизвестный путь → null', () => {
		expect(activeItem(u('/nope'))).toBeNull();
	});
});

describe('breadcrumbFor', () => {
	it('пункт группы → группа + раздел', () => {
		expect(breadcrumbFor(u('/router/ndms'))).toEqual({ group: 'Роутер', label: 'NDMS' });
		expect(breadcrumbFor(u('/services/hrneo'))).toEqual({ group: 'Сервисы', label: 'HR Neo' });
		expect(breadcrumbFor(u('/sb/engine'))).toEqual({ group: 'Sing-box', label: 'Движок' });
		expect(breadcrumbFor(u('/sb/logs'))).toEqual({
			group: 'Sing-box',
			label: 'Журнал движка',
		});
		expect(breadcrumbFor(u('/sb/geodata'))).toEqual({ group: 'Sing-box', label: 'Гео-данные' });
	});
	it('сервис на своём маршруте → Сервисы + раздел', () => {
		expect(breadcrumbFor(u('/services/freeturn'))).toEqual({ group: 'Сервисы', label: 'FreeTurn' });
		expect(breadcrumbFor(u('/services/wdtt'))).toEqual({ group: 'Сервисы', label: 'WDTT' });
	});
	it('«Все туннели» → плоский пункт без группы', () => {
		expect(breadcrumbFor(u('/tunnels'))).toEqual({ group: null, label: 'Все туннели' });
	});
	it('корень → «Обзор» без группы', () => {
		expect(breadcrumbFor(u('/'))).toEqual({ group: null, label: 'Обзор' });
	});
	it('плоский пункт → без группы', () => {
		expect(breadcrumbFor(u('/settings'))).toEqual({ group: null, label: 'Настройки' });
	});
	it('терминал (вне дерева) → метка без группы', () => {
		expect(breadcrumbFor(u('/terminal'))).toEqual({ group: null, label: 'Терминал' });
	});
	it('неизвестный путь → null', () => {
		expect(breadcrumbFor(u('/nope'))).toBeNull();
	});
});

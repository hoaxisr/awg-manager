import { describe, it, expect } from 'vitest';
import { binaryStripItems } from './binaries';
import type { FreeTurnStatus, WdttStatus } from '$lib/types';

/**
 * Форма ответа подсистемы БЕЗ инстансов — ровно та, что приходит с роутера,
 * где инстансы удалили: списки пусты, legacy-зеркала `client`/`server`
 * заполнены пустышкой, а наличие бинарей известно из install-статуса.
 */
function wdttNoInstances(binariesPresent: boolean): WdttStatus {
	const empty = { running: false, binary: '', binaryPresent: false };
	return {
		clients: [],
		servers: [],
		client: empty,
		server: empty,
		binariesPresent,
		installAvailable: true,
		installedVersion: '1.4.4-awgm+server-1.4.0-3',
		updateAvailable: false,
		installing: false,
	};
}

function ftNoInstances(binariesPresent: boolean): FreeTurnStatus {
	const empty = { running: false, binary: '', binaryPresent: false };
	return {
		clients: [],
		servers: [],
		client: empty,
		server: empty,
		binariesPresent,
		installAvailable: true,
		installedVersion: '2.1.1-2',
		updateAvailable: false,
		installing: false,
	};
}

describe('binaryStripItems: наличие бинарей — признак подсистемы, не инстанса', () => {
	it('инстансов нет, бинари на диске — продукт установлен', () => {
		// Регресс, за который платил пользователь: полоса брала признак из
		// первого клиентского инстанса. Удалив последнего клиента, он получал
		// «wdtt не установлен» при живых /opt/bin/wdtt-*, а «Установить»
		// отрабатывала успешно и молча — признак от неё не зависел.
		const items = binaryStripItems(wdttNoInstances(true), ftNoInstances(true), null, () => {});
		expect(items.map((i) => [i.name, i.binaryPresent])).toEqual([
			['wdtt', true],
			['freeturn', true],
		]);
	});

	it('инстансов нет и бинарей нет — продукт не установлен', () => {
		const items = binaryStripItems(wdttNoInstances(false), ftNoInstances(false), null, () => {});
		expect(items.map((i) => i.binaryPresent)).toEqual([false, false]);
	});

	it('статуса ещё нет — не установлен, полоса зовёт поставить', () => {
		const items = binaryStripItems(null, null, null, () => {});
		expect(items.map((i) => i.binaryPresent)).toEqual([false, false]);
	});

	it('признак живого инстанса на полосу не влияет', () => {
		// Обратная страховка: у инстанса свой `binaryPresent` (им заперт тумблер
		// запуска). Полоса на него смотреть не должна — иначе вернётся та же
		// связка «нет инстанса → нет продукта», только с другого конца.
		const wdtt = wdttNoInstances(false);
		wdtt.clients = [
			{
				id: 'default',
				name: 'Клиент',
				status: { running: false, binary: '/opt/bin/wdtt-client', binaryPresent: true },
			},
		];
		const items = binaryStripItems(wdtt, ftNoInstances(false), null, () => {});
		expect(items[0].binaryPresent).toBe(false);
	});
});

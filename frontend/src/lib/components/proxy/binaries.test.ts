import { describe, it, expect } from 'vitest';
import { binaryStripItems } from './binaries';
import type { FreeTurnStatus, OpenFluxStatus, WdttStatus } from '$lib/types';

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
		const items = binaryStripItems(wdttNoInstances(true), ftNoInstances(true), null, null, () => {});
		expect(items.map((i) => [i.name, i.binaryPresent])).toEqual([
			['wdtt', true],
			['freeturn', true],
		]);
	});

	it('инстансов нет и бинарей нет — продукт не установлен', () => {
		const items = binaryStripItems(wdttNoInstances(false), ftNoInstances(false), null, null, () => {});
		expect(items.map((i) => i.binaryPresent)).toEqual([false, false]);
	});

	it('статуса ещё нет — не установлен, полоса зовёт поставить', () => {
		const items = binaryStripItems(null, null, null, null, () => {});
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
		const items = binaryStripItems(wdtt, ftNoInstances(false), null, null, () => {});
		expect(items[0].binaryPresent).toBe(false);
	});

	it('openflux: добавляется только при archSupported=true и верно считает обновление', () => {
		const baseOf: OpenFluxStatus = {
			installed: false,
			running: false,
			archSupported: false,
			binary: '/opt/bin/openflux',
			arch: 'mips',
		};
		expect(binaryStripItems(null, null, baseOf, null, () => {})).toHaveLength(2);

		const supportedMissing: OpenFluxStatus = {
			...baseOf,
			archSupported: true,
			arch: 'aarch64',
			latestVersion: '0.17.1-awgm.1',
		};
		const missingItems = binaryStripItems(null, null, supportedMissing, null, () => {});
		expect(missingItems.map((i) => [i.name, i.binaryPresent, i.updateAvailable])).toEqual([
			['wdtt', false, false],
			['freeturn', false, false],
			['openflux', false, false],
		]);

		const withUpdate: OpenFluxStatus = {
			...supportedMissing,
			installed: true,
			version: '0.16.0',
		};
		const updateItems = binaryStripItems(null, null, withUpdate, 'openflux', () => {});
		expect(updateItems[2]).toMatchObject({
			name: 'openflux',
			binaryPresent: true,
			installing: true,
			updateAvailable: true,
			installedVersion: '0.16.0',
			installVersion: '0.17.1-awgm.1',
		});
	});
});

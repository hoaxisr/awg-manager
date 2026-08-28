// Ручки жизненного цикла инстанса по строке списка: протокол и роль строки
// решают, какой API вызвать. Держим рядом с rows.ts — та же развилка из двух
// протоколов и двух ролей, только со стороны мутаций.

import { api } from '$lib/api/client';
import type { ProxyInstanceRow } from './rows';

/** Старт/стоп инстанса. */
export async function toggleProxyInstance(row: ProxyInstanceRow, on: boolean): Promise<void> {
	if (row.protocol === 'wdtt' && row.role === 'client') {
		if (on) await api.startWdttClientInstance(row.id);
		else await api.stopWdttClientInstance(row.id);
	} else if (row.protocol === 'wdtt') {
		if (on) await api.startWdttServerInstance(row.id);
		else await api.stopWdttServerInstance(row.id);
	} else if (row.role === 'client') {
		if (on) await api.startFreeTurnClient(row.id);
		else await api.stopFreeTurnClient(row.id);
	} else {
		if (on) await api.startFreeTurnServer(row.id);
		else await api.stopFreeTurnServer(row.id);
	}
}

export async function renameProxyInstance(row: ProxyInstanceRow, name: string): Promise<void> {
	if (row.protocol === 'wdtt' && row.role === 'client') await api.renameWdttClient(row.id, name);
	else if (row.protocol === 'wdtt') await api.renameWdttServer(row.id, name);
	else if (row.role === 'client') await api.renameFreeTurnClient(row.id, name);
	else await api.renameFreeTurnServer(row.id, name);
}

/** Удаление инстанса; у клиентов вместе с ним уезжают связанные AWG-туннели. */
export async function deleteProxyInstance(
	row: ProxyInstanceRow,
): Promise<{ deletedTunnels?: string[]; tunnelErrors?: string[] }> {
	if (row.protocol === 'wdtt' && row.role === 'client') return api.deleteWdttClient(row.id);
	if (row.protocol === 'wdtt') {
		await api.deleteWdttServer(row.id);
		return {};
	}
	if (row.role === 'client') return api.deleteFreeTurnClient(row.id);
	await api.deleteFreeTurnServer(row.id);
	return {};
}

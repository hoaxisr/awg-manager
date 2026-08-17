// Полоса бинарей (PG-04..PG-10) — данные обоих продуктов одной формой.

import type { FreeTurnStatus, WdttStatus } from '$lib/types';

export type ProxyBinaryKind = 'wdtt' | 'freeturn';

export function binaryStripItems(
	wdtt: WdttStatus | null,
	ft: FreeTurnStatus | null,
	installing: ProxyBinaryKind | null,
	oninstall: (kind: ProxyBinaryKind) => void,
) {
	return [
		{
			name: 'wdtt',
			binaryPresent: wdtt?.client.binaryPresent === true,
			installAvailable: wdtt?.installAvailable === true,
			installing: installing === 'wdtt' || wdtt?.installing === true,
			updateAvailable: wdtt?.updateAvailable === true,
			installedVersion: wdtt?.installedVersion,
			installVersion: wdtt?.installVersion,
			oninstall: () => oninstall('wdtt'),
		},
		{
			name: 'freeturn',
			binaryPresent: ft?.client.binaryPresent === true,
			installAvailable: ft?.installAvailable === true,
			installing: installing === 'freeturn' || ft?.installing === true,
			updateAvailable: ft?.updateAvailable === true,
			installedVersion: ft?.installedVersion,
			installVersion: ft?.installVersion,
			oninstall: () => oninstall('freeturn'),
		},
	];
}

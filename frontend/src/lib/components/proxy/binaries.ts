import type { FreeTurnStatus, OpenFluxStatus, WdttStatus } from '$lib/types';

export interface BinaryStripItem {
	name: 'wdtt' | 'freeturn' | 'openflux';
	binaryPresent: boolean;
	installAvailable: boolean;
	installing: boolean;
	updateAvailable: boolean;
	installedVersion?: string;
	installVersion?: string;
	oninstall: () => void;
}

/**
 * Полоса установки бинарей страницы «Прокси»: по одному элементу на подсистему.
 * OpenFlux добавляется только когда статус загружен и архитектура поддерживается.
 */
export function binaryStripItems(
	wdtt: WdttStatus | null,
	ft: FreeTurnStatus | null,
	openflux: OpenFluxStatus | null,
	installing: 'wdtt' | 'freeturn' | 'openflux' | null,
	oninstall: (kind: 'wdtt' | 'freeturn' | 'openflux') => void,
): BinaryStripItem[] {
	const items: BinaryStripItem[] = [
		{
			name: 'wdtt',
			binaryPresent: wdtt?.binariesPresent === true,
			installAvailable: wdtt?.installAvailable === true,
			installing: installing === 'wdtt',
			updateAvailable: Boolean(wdtt?.binariesPresent && wdtt?.updateAvailable),
			installedVersion: wdtt?.installedVersion,
			installVersion: wdtt?.installVersion,
			oninstall: () => oninstall('wdtt'),
		},
		{
			name: 'freeturn',
			binaryPresent: ft?.binariesPresent === true,
			installAvailable: ft?.installAvailable === true,
			installing: installing === 'freeturn',
			updateAvailable: Boolean(ft?.binariesPresent && ft?.updateAvailable),
			installedVersion: ft?.installedVersion,
			installVersion: ft?.installVersion,
			oninstall: () => oninstall('freeturn'),
		},
	];

	if (openflux && openflux.archSupported) {
		const updateAvailable = Boolean(
			openflux.installed &&
				openflux.version &&
				openflux.latestVersion &&
				openflux.version !== openflux.latestVersion,
		);
		items.push({
			name: 'openflux',
			binaryPresent: openflux.installed,
			installAvailable: openflux.archSupported,
			installing: installing === 'openflux',
			updateAvailable,
			installedVersion: openflux.version,
			installVersion: openflux.latestVersion,
			oninstall: () => oninstall('openflux'),
		});
	}

	return items;
}

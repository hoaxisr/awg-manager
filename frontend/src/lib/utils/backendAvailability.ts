// AWG 3.0 device params are only honoured by the 3.x AmneziaWG kernel module.
// Older modules ignore the netlink attributes without an error, so the tunnel
// would look configured while running plain AWG 2.0 — gate the editor instead.
export function supportsAwg3(kernelModuleLoadedVersion: string | undefined): boolean {
	return /^3\./.test(kernelModuleLoadedVersion ?? '');
}

// Maps the backend's `nativewgReason` (from system/info) to a user-facing
// explanation shown next to the disabled NativeWG option, so it no longer
// greys out silently. Empty reason → no hint (NativeWG is available).
export function nativewgUnavailableHint(reason: string | undefined): string {
	switch (reason) {
		case 'no-component':
			return 'Не установлен компонент WireGuard. Установите его на роутере: Общие настройки → Изменить набор компонентов → WireGuard, затем перезагрузите роутер.';
		case 'no-obfuscation':
			return 'NativeWG недоступен: прошивка без нативного WireGuard ASC и не загружен модуль awg_proxy.';
		default:
			return '';
	}
}

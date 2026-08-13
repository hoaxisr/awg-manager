// AWG 3.0 device params are only honoured by the 3.x AmneziaWG kernel module.
// Older modules ignore the netlink attributes without an error, so the tunnel
// would look configured while running plain AWG 2.0 — gate the editor instead.
export function supportsAwg3(kernelModuleLoadedVersion: string | undefined): boolean {
	return /^3\./.test(kernelModuleLoadedVersion ?? '');
}

// AWG 3.1 added two device flags (RandomTrailers, DisableCookies) that a 3.0
// module ignores without an error, exactly the way a 2.x module ignores the 3.0
// params — so they need a floor of their own rather than reusing supportsAwg3.
export function supportsAwg31(kernelModuleLoadedVersion: string | undefined): boolean {
	const m = /^(\d+)\.(\d+)\./.exec(kernelModuleLoadedVersion ?? '');
	if (!m) return false;
	const major = Number(m[1]);
	const minor = Number(m[2]);
	return major > 3 || (major === 3 && minor >= 1);
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

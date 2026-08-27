// AWG 3.0 device params are only honoured by the 3.x AmneziaWG kernel module.
// Older modules ignore the netlink attributes without an error, so the tunnel
// would look configured while running plain AWG 2.0 — gate the editor instead.
export function supportsAwg3(kernelModuleLoadedVersion: string | undefined): boolean {
	return /^3\./.test(kernelModuleLoadedVersion ?? '');
}

// AWG 3.1 over NativeWG runs through awg_proxy, whose 1.4.0 build adds header
// protection (HP_KEY) + random trailers (RT). Older proxies silently ignore
// those tokens, so gate the NativeWG awg3 editor on the loaded proxy version —
// the NativeWG analogue of supportsAwg3 for the kernel module.
export function supportsAwg31Proxy(awgProxyVersion: string | undefined): boolean {
	const m = /^(\d+)\.(\d+)/.exec(awgProxyVersion ?? '');
	if (!m) return false;
	const major = Number(m[1]);
	const minor = Number(m[2]);
	return major > 1 || (major === 1 && minor >= 4);
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

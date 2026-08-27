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

// The .ko shipped with the app is newer than the one currently in the kernel.
// awg_proxy is only reloaded when it has no live slots (rmmod would kill every
// running tunnel's proxy), so with a tunnel up the upgrade waits for a reboot —
// and until then AWG 3.1 stays unavailable with no visible reason.
export function awgProxyOutdated(
	loaded: string | undefined,
	expected: string | undefined,
): boolean {
	if (!loaded || !expected) return false;
	const parse = (v: string) => v.split('.').map(Number);
	const [a, b] = [parse(loaded), parse(expected)];
	for (let i = 0; i < Math.max(a.length, b.length); i++) {
		const l = a[i] ?? 0;
		const e = b[i] ?? 0;
		if (l !== e) return l < e;
	}
	return false;
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

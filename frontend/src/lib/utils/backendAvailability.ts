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

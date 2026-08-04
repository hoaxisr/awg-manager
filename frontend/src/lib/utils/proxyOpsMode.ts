/** Instance entered ops panel after first successful start or explicit enable. */
export function proxyInOpsMode(opts: {
	running?: boolean;
	startedAt?: string;
	enabled?: boolean;
	/** Optional extra signal (e.g. link already generated on server). */
	setupComplete?: boolean;
}): boolean {
	return !!(opts.running || opts.startedAt?.trim() || opts.enabled || opts.setupComplete);
}

/**
 * Сервер FreeTurn/WDTT: ops-панель после первого запуска или если ссылка уже сгенерирована
 * в текущей сессии. startedAt сохраняется на бэкенде — не сбрасывается при уходе со страницы.
 */
export function proxyServerOpsMode(opts: {
	running?: boolean;
	startedAt?: string;
	enabled?: boolean;
	generatedLink?: string;
}): boolean {
	if (!proxyInOpsMode(opts)) return false;
	if (opts.generatedLink?.trim()) return true;
	if (opts.startedAt?.trim()) return true;
	// После reboot: enabled=true в конфиге, но startedAt/genWG в UI ещё пустые.
	if (opts.enabled) return true;
	return false;
}

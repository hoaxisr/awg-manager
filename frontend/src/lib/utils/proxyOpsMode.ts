/** Instance entered ops panel after first successful start, autostart, or saved setup. */
export function proxyInOpsMode(opts: {
	running?: boolean;
	startedAt?: string;
	enabled?: boolean;
	/** Конфиг заполнен — ops-панель и после ручного стопа (startedAt/enabled сбрасываются). */
	setupComplete?: boolean;
}): boolean {
	return !!(opts.running || opts.startedAt?.trim() || opts.enabled || opts.setupComplete);
}

/** Клиент WDTT/FreeTurn: ops после первого запуска или когда профиль полностью настроен. */
export function proxyClientOpsMode(opts: {
	running?: boolean;
	startedAt?: string;
	enabled?: boolean;
	setupComplete?: boolean;
}): boolean {
	return proxyInOpsMode(opts);
}

/**
 * Сервер FreeTurn/WDTT: ops после первого запуска, сгенерированной ссылки или сохранённого конфига.
 */
export function proxyServerOpsMode(opts: {
	running?: boolean;
	startedAt?: string;
	enabled?: boolean;
	generatedLink?: string;
	setupComplete?: boolean;
}): boolean {
	if (opts.setupComplete) return true;
	if (!proxyInOpsMode(opts)) return false;
	if (opts.generatedLink?.trim()) return true;
	if (opts.startedAt?.trim()) return true;
	if (opts.enabled) return true;
	return false;
}

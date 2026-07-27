/**
 * Extract a human-readable message from an unknown thrown value.
 *
 * `catch` clauses bind their variable as `unknown` under TS strict mode, so a
 * thrown value is not guaranteed to be an `Error`. This narrows it: a real
 * `Error` with a non-empty message yields that message, everything else (empty
 * message, string throw, plain object, `undefined`) falls back to `fallback`.
 */
export function errorMessage(e: unknown, fallback = 'Ошибка'): string {
	if (e instanceof Error && e.message) {
		return e.message;
	}
	if (typeof e === 'string' && e) {
		return e;
	}
	return fallback;
}

/**
 * Like {@link errorMessage} but keeps the raw stringified value instead of a
 * fallback: an `Error` yields its `message`, anything else yields `String(e)`
 * (with `null`/`undefined` collapsed to `''`). Used where the caller supplies
 * its own prefix/fallback (e.g. `'WDTT: ' + errText(e)`, `errText(e) || '…'`).
 */
export function errText(e: unknown): string {
	return e instanceof Error ? e.message : String(e ?? '');
}

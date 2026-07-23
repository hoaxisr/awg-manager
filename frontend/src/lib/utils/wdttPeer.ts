/** Сравнение WDTT peer'ов (host:port); порт по умолчанию — DTLS 56000. Пустые строки равны. */
export function peersEqual(a: string, b: string): boolean {
	const norm = (p: string) => {
		const t = p.trim();
		if (!t) return '';
		return t.includes(':') ? t : `${t}:56000`;
	};
	return norm(a) === norm(b);
}

/** Parse host:port from proxy listen address. */
export function parseListenHostPort(
	listen: string,
	defaultHost = '127.0.0.1'
): { host: string; port: number } | null {
	const raw = listen.trim();
	if (!raw) return null;
	let host = defaultHost;
	let portPart = raw;
	if (raw.startsWith(':')) {
		portPart = raw.slice(1);
	} else {
		const idx = raw.lastIndexOf(':');
		if (idx >= 0) {
			host = raw.slice(0, idx) || defaultHost;
			portPart = raw.slice(idx + 1);
		}
	}
	if (host === 'localhost') host = '127.0.0.1';
	const port = Number(portPart);
	if (!Number.isInteger(port) || port <= 0 || port > 65535) return null;
	return { host: host || defaultHost, port };
}

/** Replace port in listen address, preserving host. */
export function setListenPort(listen: string, port: number, defaultHost = '0.0.0.0'): string {
	const parsed = parseListenHostPort(listen, defaultHost);
	const host = parsed?.host || defaultHost;
	return `${host}:${port}`;
}

export function listenPortNumber(listen: string, fallback: number): number {
	return parseListenHostPort(listen)?.port ?? fallback;
}

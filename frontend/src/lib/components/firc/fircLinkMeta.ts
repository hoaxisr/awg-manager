export interface FircLinkMeta {
	proto: string;
	security: string;
	transport: string;
}

/** Протокол, защита и транспорт для бейджей карточки — по первой ссылке туннеля. */
export function fircLinkMeta(firstLink: string): FircLinkMeta {
	if (!firstLink) {
		return { proto: 'VLESS', security: 'Reality', transport: '' };
	}
	const lower = firstLink.toLowerCase();
	const isLocalFpBridge = /^socks5?:\/\/127\.0\.0\.1:19[1-4]\d\d\b/.test(lower);
	const isFiniteProto =
		lower.startsWith('fp://') ||
		lower.startsWith('finiteproto://') ||
		lower.includes('mode=finiteproto') ||
		lower.includes('type=finiteproto') ||
		isLocalFpBridge;
	let proto = 'VLESS';
	let security = '';
	let transport = '';
	if (isFiniteProto) {
		proto = 'VLESS';
		security = 'TLS';
		transport = 'FiniteProto';
	} else if (
		lower.startsWith('tt://') ||
		((lower.startsWith('https://') || lower.startsWith('http://')) && lower.includes('d='))
	) {
		proto = 'TrustTunnel';
		security = 'TLS';
	} else if (lower.startsWith('trojan://')) {
		proto = 'Trojan';
		security = 'TLS';
	} else if (lower.startsWith('hysteria2://') || lower.startsWith('hy2://')) {
		proto = 'Hysteria2';
		security = 'QUIC';
	} else if (lower.startsWith('ss://')) {
		proto = 'Shadowsocks';
	} else if (lower.startsWith('naive+https://') || lower.startsWith('naive://')) {
		proto = 'Naive';
		security = 'TLS';
	} else if (lower.startsWith('socks5://') || lower.startsWith('socks://')) {
		proto = 'SOCKS5';
	} else {
		proto = 'VLESS';
		if (lower.includes('security=reality') || lower.includes('pbk=')) {
			security = 'Reality';
		} else if (lower.includes('security=tls')) {
			security = 'TLS';
		}
	}
	if (!isFiniteProto) {
		if (lower.includes('type=xhttp') || lower.includes('type=splithttp')) {
			transport = 'XHTTP';
		} else if (lower.includes('type=ws')) {
			transport = 'WS';
		} else if (lower.includes('type=grpc')) {
			transport = 'gRPC';
		} else if (lower.includes('type=httpupgrade')) {
			transport = 'HTTPUpgrade';
		}
	}
	return { proto, security, transport };
}

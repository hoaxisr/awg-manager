import { describe, expect, it } from 'vitest';
import { prepareQrPayload, renderCrispQrDataUrl } from './qrSvg';

describe('qrSvg', () => {
	it('compacts pretty-printed JSON payloads before QR encoding', () => {
		const pretty = '{\n  "protocol": "vless",\n  "port": 443,\n  "network": "xhttp"\n}';
		expect(prepareQrPayload(pretty)).toBe('{"protocol":"vless","port":443,"network":"xhttp"}');
	});

	it('leaves VLESS URLs untouched', () => {
		const url = 'vless://00000000-1111-4222-8333-444444444444@example.com:443?type=xhttp#Phone';
		expect(prepareQrPayload(url)).toBe(url);
	});

	it('renders crispEdges vector SVG data URL for long VLESS XHTTP URIs', async () => {
		const uri =
			'vless://00000000-1111-4222-8333-444444444444@connect.example.netcraze.club:443' +
			'?encryption=none&security=tls&sni=connect.example.netcraze.club&alpn=h2,http/1.1' +
			'&type=xhttp&host=connect.example.netcraze.club&path=%2Fcdn-bridge&mode=stream-one' +
			'&packetEncoding=xudp&extra=%7B%22xPaddingBytes%22%3A%22100-500%22%7D#Phone';
		const dataUrl = await renderCrispQrDataUrl(uri);
		expect(dataUrl.startsWith('data:image/svg+xml;charset=utf-8,')).toBe(true);
		const svg = decodeURIComponent(dataUrl.slice('data:image/svg+xml;charset=utf-8,'.length));
		expect(svg).toContain('shape-rendering="crispEdges"');
		expect(svg).toContain('<svg');
	});

	it('returns empty string for blank input', async () => {
		expect(await renderCrispQrDataUrl('   ')).toBe('');
	});
});

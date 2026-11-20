import QRCode from 'qrcode';

export function prepareQrPayload(rawContent: string): string {
	const trimmed = rawContent.trim();
	if (trimmed.startsWith('{') && trimmed.endsWith('}')) {
		try {
			return JSON.stringify(JSON.parse(trimmed));
		} catch {
			// Preserve raw text if it is not valid JSON.
		}
	}
	return trimmed;
}

export async function renderCrispQrDataUrl(rawContent: string): Promise<string> {
	const payload = prepareQrPayload(rawContent);
	if (!payload) return '';
	const svg = await QRCode.toString(payload, {
		type: 'svg',
		margin: 4,
		errorCorrectionLevel: payload.length <= 360 ? 'M' : 'L',
		color: { dark: '#000000', light: '#ffffff' },
	});
	const crispSvg = svg.includes('shape-rendering=')
		? svg
		: svg.replace('<svg ', '<svg shape-rendering="crispEdges" ');
	return 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(crispSvg);
}

import { m } from '$lib/i18n';
import { downloadBlob } from '$lib/utils/download';

export function singboxConfigFilename(): string {
	const date = new Date().toISOString().slice(0, 10);
	return `singbox-config-${date}.json`;
}

/** Скачать уже загруженный текст конфига sing-box. */
export function downloadSingboxConfigText(json: string): void {
	const text = json.trim();
	if (!text) {
		throw new Error(m.singbox_routing_json_empty());
	}
	const blob = new Blob([text], { type: 'application/json;charset=utf-8' });
	downloadBlob(blob, singboxConfigFilename());
}

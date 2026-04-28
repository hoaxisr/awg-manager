/**
 * Copy text to the system clipboard. Uses the modern Async Clipboard API
 * when available (HTTPS / localhost). Falls back to a hidden <textarea> +
 * document.execCommand('copy') for plain HTTP contexts (e.g. AWGM running
 * on a Keenetic router LAN address with no TLS), where navigator.clipboard
 * is undefined or throws.
 *
 * Returns true on success, false on failure. Callers should surface the
 * result to the user (notifications.success / notifications.error).
 */
export async function copyToClipboard(text: string): Promise<boolean> {
	if (navigator.clipboard && window.isSecureContext) {
		try {
			await navigator.clipboard.writeText(text);
			return true;
		} catch {
			// fall through to legacy fallback
		}
	}

	try {
		const ta = document.createElement('textarea');
		ta.value = text;
		ta.setAttribute('readonly', '');
		ta.style.position = 'fixed';
		ta.style.top = '0';
		ta.style.left = '0';
		ta.style.opacity = '0';
		ta.style.pointerEvents = 'none';
		document.body.appendChild(ta);
		ta.select();
		ta.setSelectionRange(0, text.length);
		const ok = document.execCommand('copy');
		document.body.removeChild(ta);
		return ok;
	} catch {
		return false;
	}
}

export const obfProfileHints: Record<string, string> = {
	none: 'Без маскировки — только для отладки',
	rtpopus: 'Маскирует трафик под RTP/Opus (базовый профиль)',
	rtpopus2: 'Рекомендуется — усиленная маскировка, хороший баланс',
	rtpopus3: 'Максимальная маскировка, чуть больше накладных расходов'
};

export function randomObfKeyHex(): string {
	const bytes = new Uint8Array(32);
	crypto.getRandomValues(bytes);
	return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

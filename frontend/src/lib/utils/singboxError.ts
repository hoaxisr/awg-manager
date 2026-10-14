import { m, type UiText } from '$lib/i18n';

// Отказ NO_SPACE бэкенд пишет по-русски — для UI берём строку каталога
// отложенным вызовом, чтобы она следовала за сменой языка (#964).
export function singboxErrorText(e: unknown): UiText {
	if ((e as { body?: { code?: string } })?.body?.code === 'NO_SPACE') {
		return () => m.singbox_error_no_space();
	}
	return e instanceof Error ? e.message : String(e);
}

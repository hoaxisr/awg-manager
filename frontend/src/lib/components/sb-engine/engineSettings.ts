// Сохранение настроек движка со страницы «Движок».
//
// Пайплайн общий с sb-router (`mergeAndSaveSettings`: свежий GET → merge → PUT
// → loadAll) — PUT уносит полный объект, и эхо устаревшего стора молча
// откатывало бы чужие правки. Здесь только обёртка с тостом об ошибке, чтобы
// карточки страницы не разъезжались в формулировках.
import { mergeAndSaveSettings } from '$lib/components/sb-router/settingsActions';
import { notifications } from '$lib/stores/notifications';
import type { SingboxRouterSettings } from '$lib/types';

export async function applyEngineSettings(
	patch: Partial<SingboxRouterSettings>,
): Promise<boolean> {
	try {
		await mergeAndSaveSettings(patch);
		return true;
	} catch (e) {
		notifications.error(`Не удалось сохранить: ${e instanceof Error ? e.message : String(e)}`);
		return false;
	}
}

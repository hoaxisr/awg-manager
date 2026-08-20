/**
 * Статусы скриптов текущего каталога: опрос при загрузке списка и выполнение
 * действий (start/stop/restart/run) с обновлением статуса конкретного файла.
 */

import { api, type SystemFileEntry, type FileSystemScriptStatus } from '$lib/api/client';
import { notifications } from '$lib/stores/notifications';
import { errorMessage } from '$lib/utils/errorMessage';
import { getFileTypeInfo } from './fileIcons';
import type { ScriptAction } from './types';

export interface ScriptStatusesController {
	/** Статус по пути файла; только для тех, кого бэкенд признал скриптом. */
	readonly statuses: Record<string, FileSystemScriptStatus>;
	/** Путь файла, для которого сейчас выполняется действие. */
	readonly runningPath: string | null;
	/** Сбросить карту статусов (смена каталога). */
	reset(): void;
	/** Опросить статусы для скриптов из списка. */
	load(items: SystemFileEntry[]): Promise<void>;
	/** Выполнить действие над скриптом и обновить его статус. */
	run(entry: SystemFileEntry, action: ScriptAction): Promise<void>;
}

export function createScriptStatuses(): ScriptStatusesController {
	let statuses = $state<Record<string, FileSystemScriptStatus>>({});
	let runningPath = $state<string | null>(null);

	return {
		get statuses() {
			return statuses;
		},
		get runningPath() {
			return runningPath;
		},
		reset() {
			statuses = {};
		},
		async load(items: SystemFileEntry[]) {
			const scriptItems = items.filter(
				(e) => !e.isDir && (getFileTypeInfo(e.name, e.isDir).kind === 'script' || e.mode.includes('x')),
			);
			for (const item of scriptItems) {
				try {
					const st = await api.systemFilesScriptStatus(item.path);
					if (st.isScript) {
						statuses = { ...statuses, [item.path]: st };
					}
				} catch {
					// ignore
				}
			}
		},
		async run(entry: SystemFileEntry, action: ScriptAction) {
			runningPath = entry.path;
			try {
				const res = await api.systemFilesScriptAction({ path: entry.path, action });
				if (res.ok) {
					const actionName = action === 'start' || action === 'run' ? 'запущен' : action === 'restart' ? 'перезапущен' : 'остановлен';
					notifications.success(`Скрипт «${entry.name}» ${actionName}`);
				} else {
					notifications.error(res.error || 'Ошибка выполнения');
				}
				const st = await api.systemFilesScriptStatus(entry.path);
				statuses = { ...statuses, [entry.path]: st };
			} catch (e) {
				notifications.error(errorMessage(e, 'Ошибка выполнения'));
			} finally {
				runningPath = null;
			}
		},
	};
}

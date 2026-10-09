/**
 * updateSnapshots — снимки настроек перед обновлением (карточка «Резервное
 * копирование»). Снимок появляется при автообновлении и уходит по сроку без
 * участия страницы, поэтому список обновляется по `resource:invalidated` с
 * ключом "updateSnapshots" (backend ResourceUpdateSnapshots).
 */
import { api } from '$lib/api/client';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';
import type { UpdateSnapshotsData } from '$lib/types';

export const updateSnapshots: PollingStore<UpdateSnapshotsData> = createPollingStore<UpdateSnapshotsData>(
	() => api.listUpdateSnapshots(),
	{ staleTime: 5_000, pollInterval: 0 }
);

registerStore('updateSnapshots', updateSnapshots);

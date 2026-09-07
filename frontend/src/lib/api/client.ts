// Фасад API-клиента. Доменные методы разнесены по слоям client*.ts
// (цепочка наследования от CoreClient); публичная поверхность не менялась:
// `api` и сопутствующие экспорты доступны по прежнему пути $lib/api/client.
import { Awg3Client } from './clientAwg3';
import type { AwgAnalyzeData } from '$lib/types';

export { ApiGatewayError } from './clientCore';
export type { TrafficPeriod } from './clientCore';
// Реэкспорт для существующих импортов из '$lib/api/client'; сами типы
// объявлены в $lib/types/systemTools.
export type {
	SystemFileRoot,
	SystemFileEntry,
	FileSystemScriptStatus,
	SystemServiceItem,
	SystemOpkgPackage,
	SystemPortBinding,
	SystemProcSnapshot,
	SystemProcessItem,
} from '$lib/types';

class ApiClient extends Awg3Client {
	// Анализ .conf на бэкенде: версия, поля без ключей, ошибки совместимости.
	// tunnelId подставляет ключи из хранилища, если их нет в тексте (#865).
	async analyzeAwgConf(conf: string, tunnelId?: string): Promise<AwgAnalyzeData> {
		return this.request<AwgAnalyzeData>('/awg/analyze', {
			method: 'POST',
			body: JSON.stringify(tunnelId ? { conf, tunnelId } : { conf }),
		});
	}
}

export const api = new ApiClient();

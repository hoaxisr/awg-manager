// Фасад API-клиента. Доменные методы разнесены по слоям client*.ts
// (цепочка наследования от CoreClient); публичная поверхность не менялась:
// `api` и сопутствующие экспорты доступны по прежнему пути $lib/api/client.
import { FircClient } from './clientFirc';
import type { AwgAnalyzeData } from '$lib/types';
import type { UplinkEntry, UplinkPolicy, UplinkSnapshot, WANDNSConfig } from '$lib/types/uplink';

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

class ApiClient extends FircClient {
	async uplinkDNS(): Promise<WANDNSConfig> { return this.request('/uplink/dns'); }
	async uplinkSaveDNS(provider: string, revision: number, servers: string[]): Promise<WANDNSConfig> {
		return this.request('/uplink/dns', { method: 'PUT', body: JSON.stringify({ provider, revision, servers }) });
	}
	async uplinkSnapshot(): Promise<UplinkSnapshot> { return this.request('/uplink/policies'); }
	async uplinkSave(policy: UplinkPolicy): Promise<UplinkPolicy> {
		return this.request('/uplink/policies', { method: 'PUT', body: JSON.stringify(policy) });
	}
	async uplinkDelete(id: string, revision: number): Promise<void> {
		return this.request('/uplink/policies', { method: 'DELETE', body: JSON.stringify({ id, revision }) });
	}
	async uplinkSwitch(id: string, provider: string, revision: number): Promise<UplinkEntry[]> {
		return this.request('/uplink/switch', { method: 'POST', body: JSON.stringify({ id, provider, revision }) });
	}
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

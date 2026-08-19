// Фасад API-клиента. Доменные методы разнесены по слоям client*.ts
// (цепочка наследования от CoreClient); публичная поверхность не менялась:
// `api` и сопутствующие экспорты доступны по прежнему пути $lib/api/client.
import { Awg3Client } from './clientAwg3';

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

class ApiClient extends Awg3Client {}

export const api = new ApiClient();

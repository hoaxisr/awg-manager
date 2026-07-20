// Баррель доменных файлов типов: путь импорта $lib/types сохраняется,
// содержимое разнесено по темам в ./types/* (см. PR реструктуризации).
export * from './types/tunnels';
export * from './types/routing';
export * from './types/servers';
export * from './types/system';
export * from './types/deviceproxy';
export * from './types/diagnostics';
export * from './types/singbox';
export * from './types/sbRouter';
export * from './types/subscriptions';
export * from './types/freeturn';

/** Imported AWG3 (sing-box awg endpoint) tunnel — key-free API projection. */
export interface Awg3Tunnel {
	id: string;
	tag: string;
	host: string;
	headerProtection: boolean;
}

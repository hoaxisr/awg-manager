export interface TelemtConfig {
	enabled: boolean;
	mode?: 'direct' | 'web';
	port: number;
	listenIp: string;
	secret: string;
	tlsDomain: string;
	webHost?: string;
	webCarrier?: string;
	webDecoy?: string;
	upstreamDevice?: string;
}

export interface TelemtStatus {
	installed: boolean;
	running: boolean;
	pid?: number;
	version?: string;
	latestVersion?: string;
	updateAvailable: boolean;
	archSupported?: boolean;
	binary?: string;
	arch?: string;
	source?: 'managed' | 'opkg' | 'external' | string;
	link?: string;
	error?: string;
	config?: TelemtConfig;
}

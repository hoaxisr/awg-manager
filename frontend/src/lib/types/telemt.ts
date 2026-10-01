export interface TelemtConfig {
	enabled: boolean;
	port: number;
	listenIp: string;
	secret: string;
	tlsDomain: string;
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

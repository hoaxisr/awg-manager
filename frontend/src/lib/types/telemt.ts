export interface TelemtConfig {
	enabled: boolean;
	interface: string;
	port: number;
	secret: string;
	tlsDomain: string;
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
	source?: string;
	link?: string;
	error?: string;
	config?: TelemtConfig;
}

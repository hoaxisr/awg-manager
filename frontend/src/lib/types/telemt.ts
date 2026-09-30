export interface TelemtStatus {
	installed: boolean;
	running: boolean;
	pid?: number;
	version?: string;
	latestVersion?: string;
	updateAvailable: boolean;
	binary?: string;
	arch?: string;
	error?: string;
}

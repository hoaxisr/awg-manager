const STORAGE_KEY = 'awg.freeturn.captchaAutoOpen';

/** Whether to auto-open the captcha modal when freeturn needs manual captcha. Default: true. */
export function readCaptchaAutoOpen(): boolean {
	if (typeof localStorage === 'undefined') return true;
	try {
		return localStorage.getItem(STORAGE_KEY) !== '0';
	} catch {
		return true;
	}
}

export function writeCaptchaAutoOpen(enabled: boolean): void {
	try {
		localStorage.setItem(STORAGE_KEY, enabled ? '1' : '0');
	} catch {
		// private mode etc.
	}
}

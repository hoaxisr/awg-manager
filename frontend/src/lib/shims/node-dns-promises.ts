// Browser shim for optional Node-only imports (e.g. vinejs runtime).
// This module should never be called in client code paths.
const notAvailable = () => {
	throw new Error('node:dns/promises is not available in the browser');
};

export const lookup = notAvailable;
export const resolve = notAvailable;
export const resolve4 = notAvailable;
export const resolve6 = notAvailable;
export const reverse = notAvailable;

export class Resolver {
	constructor() {
		notAvailable();
	}
}

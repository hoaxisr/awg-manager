/**
 * Browser shim for node:dns/promises.
 * Used to silence Vite externalization warnings from dependencies that
 * conditionally import Node DNS APIs on the server only.
 */
export async function resolve4(): Promise<string[]> {
	return [];
}

export async function resolve6(): Promise<string[]> {
	return [];
}


import type { SystemFileEntry } from '$lib/api/client';

export type TreeDir = {
	path: string;
	name: string;
	expanded: boolean;
	loading: boolean;
	children: TreeDir[];
};

export type CtxMenu = {
	x: number;
	y: number;
	entry: SystemFileEntry | null;
};

export type ScriptAction = 'start' | 'stop' | 'restart' | 'run';

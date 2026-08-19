export type CreateMode = 'template' | 'clone' | 'custom';

export interface CreateServiceFormState {
	mode: CreateMode;
	tplName: string;
	tplPriority: number;
	tplDesc: string;
	tplProc: string;
	tplArgs: string;
	cloneSourceScript: string;
	cloneTargetName: string;
	clonePriority: number;
	customScriptName: string;
	customScriptContent: string;
}

/** Собирает init.d-скрипт на базе rc.func из полей конструктора. */
export function buildInitScript(procName: string, desc: string, args: string): string {
	return `#!/bin/sh

ENABLED=yes
PROCS="${procName}"
ARGS="${args}"
PREARGS=""
DESC="${desc}"
PATH=/opt/sbin:/opt/bin:/opt/usr/bin:/usr/sbin:/usr/bin:/sbin:/bin

. /opt/etc/init.d/rc.func
`;
}

export const DEFAULT_CUSTOM_SCRIPT = buildInitScript('my-daemon', 'My Custom Service', '');

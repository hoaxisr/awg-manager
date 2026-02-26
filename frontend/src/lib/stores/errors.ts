import { writable, derived } from 'svelte/store';

export interface AppError {
    id: string;
    message: string;
    code?: string;
    context?: string;
    timestamp: Date;
    dismissed: boolean;
}

function createErrorStore() {
    const errors = writable<AppError[]>([]);

    function generateId(): string {
        return `err_${Date.now()}_${Math.random().toString(36).substr(2, 9)}`;
    }

    return {
        subscribe: errors.subscribe,

        /**
         * Add a new error
         */
        add(message: string, options?: { code?: string; context?: string }) {
            const error: AppError = {
                id: generateId(),
                message,
                code: options?.code,
                context: options?.context,
                timestamp: new Date(),
                dismissed: false
            };

            errors.update(errs => [...errs, error]);

            // Auto-dismiss after 10 seconds
            setTimeout(() => {
                this.dismiss(error.id);
            }, 10000);

            return error.id;
        },

        /**
         * Dismiss an error
         */
        dismiss(id: string) {
            errors.update(errs =>
                errs.map(e => e.id === id ? { ...e, dismissed: true } : e)
            );
        },

        /**
         * Clear all errors
         */
        clear() {
            errors.set([]);
        },

        /**
         * Get active (non-dismissed) errors
         */
        get active() {
            return derived(errors, $errors =>
                $errors.filter(e => !e.dismissed)
            );
        },

        /**
         * Handle API error response
         */
        handleApiError(error: unknown, context?: string) {
            let message = 'Unknown error occurred';

            if (error instanceof Error) {
                message = error.message;
            } else if (typeof error === 'object' && error !== null) {
                const obj = error as { error?: string; message?: string };
                message = obj.error || obj.message || message;
            } else if (typeof error === 'string') {
                message = error;
            }

            return this.add(message, { context });
        }
    };
}

export const errorStore = createErrorStore();

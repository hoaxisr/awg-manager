// See https://svelte.dev/docs/kit/types#app.d.ts

// swagger-ui-dist ships a plain UMD bundle with no TypeScript types.
declare module 'swagger-ui-dist/swagger-ui-bundle.js' {
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	const SwaggerUIBundle: any;
	export = SwaggerUIBundle;
}

declare namespace App {}

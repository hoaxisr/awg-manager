// Focused lint config: enforce explicit typing over `any` and strip redundant
// `as` casts. Intentionally NARROW — only the type-safety rules, not the full
// recommended set — so runs stay signal-dense for the typing cleanup.
import tseslint from 'typescript-eslint';
import svelteParser from 'svelte-eslint-parser';

export default tseslint.config(
	{
		ignores: [
			'build/',
			'.svelte-kit/',
			'dist/',
			'node_modules/',
			'static/',
			'scripts/',
			// Рабочий каталог плагина remember: свой .gitignore у него есть, но
			// eslint идёт не по нему и падал на разборе .ts вне tsconfig — гейт
			// был красным независимо от правок (F328).
			'.remember/',
			'**/*.cjs',
			// Генерированный вывод npm run gen:api — не линтится, как и любой codegen.
			'src/lib/api/schemas.gen.ts',
		],
	},
	{
		// Type-aware pass for plain TS: projectService lets
		// no-unnecessary-type-assertion consult the checker. Fast (~15s).
		files: ['**/*.ts'],
		plugins: { '@typescript-eslint': tseslint.plugin },
		languageOptions: {
			parser: tseslint.parser,
			parserOptions: {
				// tailwind.config.ts sits outside the tsconfig project graph
				// (vite.config.ts is inside it); lint it via the default project.
				projectService: { allowDefaultProject: ['tailwind.config.ts'] },
				tsconfigRootDir: import.meta.dirname,
			},
		},
		rules: {
			'@typescript-eslint/no-explicit-any': 'error',
			'@typescript-eslint/no-unnecessary-type-assertion': 'error',
		},
	},
	{
		// Svelte components: syntactic-only. Enabling projectService here makes
		// the run take 15+ minutes (each component is type-checked through the
		// svelte2tsx pipeline), so type-aware rules for .svelte are left to
		// `npm run check` (svelte-check) and only the syntactic `any` ban runs.
		files: ['**/*.svelte'],
		plugins: { '@typescript-eslint': tseslint.plugin },
		languageOptions: {
			parser: svelteParser,
			parserOptions: {
				parser: tseslint.parser,
				extraFileExtensions: ['.svelte'],
			},
		},
		rules: {
			'@typescript-eslint/no-explicit-any': 'error',
		},
	},
);

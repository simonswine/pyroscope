import { mkdir, readFile, writeFile, rm } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import ts from '../../../ui/node_modules/typescript/lib/typescript.js';

await mkdir(new URL('./.test/', import.meta.url), { recursive: true });
try {
  const source = await readFile(new URL('./pprof.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } });
  await writeFile(new URL('./.test/pprof.mjs', import.meta.url), outputText);
  const result = spawnSync(process.execPath, ['--test', 'pprof.test.mjs'], { cwd: new URL('.', import.meta.url), stdio: 'inherit' });
  process.exitCode = result.status ?? 1;
} finally {
  await rm(new URL('./.test/', import.meta.url), { recursive: true, force: true });
}

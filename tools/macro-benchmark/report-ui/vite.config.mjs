import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(fileURLToPath(import.meta.url));
// Run with the Vite installed in ../../../ui (see README).
export default {
  root,
  build: {
    outDir: resolve(root, '..', 'report_assets'),
    emptyOutDir: false,
    cssCodeSplit: false,
    rollupOptions: {
      input: resolve(root, 'main.tsx'),
      output: {
        format: 'iife',
        inlineDynamicImports: true,
        entryFileNames: 'report.js',
        assetFileNames: 'report[extname]',
      },
    },
  },
};

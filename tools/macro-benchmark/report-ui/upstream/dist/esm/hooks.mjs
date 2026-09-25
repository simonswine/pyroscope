import { useState, useEffect } from 'react';
import { ColorSchemeDiff, ColorScheme } from './types.mjs';

"use strict";
function useColorScheme(dataContainer) {
  const defaultColorScheme = (dataContainer == null ? void 0 : dataContainer.isDiffFlamegraph()) ? ColorSchemeDiff.Default : ColorScheme.PackageBased;
  const [colorScheme, setColorScheme] = useState(defaultColorScheme);
  useEffect(() => {
    setColorScheme(defaultColorScheme);
  }, [defaultColorScheme]);
  return [colorScheme, setColorScheme];
}

export { useColorScheme };
//# sourceMappingURL=hooks.mjs.map

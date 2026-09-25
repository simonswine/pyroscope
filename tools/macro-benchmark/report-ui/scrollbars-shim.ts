import * as scrollbars from "./node_modules/react-custom-scrollbars-2/lib/index.js";
// react-custom-scrollbars-2 is CJS with both default and named exports. The
// library-mode bundler otherwise treats the entire export object as a React component.
export default (scrollbars as unknown as { Scrollbars: unknown }).Scrollbars;

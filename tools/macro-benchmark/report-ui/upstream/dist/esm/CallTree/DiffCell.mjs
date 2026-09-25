import { jsx } from 'react/jsx-runtime';

"use strict";
function DiffCell({ value, theme }) {
  if (value === void 0) {
    return /* @__PURE__ */ jsx("span", { children: "-" });
  }
  let displayValue;
  let color;
  if (value === Infinity) {
    displayValue = "new";
    color = theme.colors.success.text;
  } else if (value === -100) {
    displayValue = "removed";
    color = theme.colors.error.text;
  } else {
    displayValue = `${value > 0 ? "+" : ""}${value.toFixed(2)}%`;
    color = value > 0 ? theme.colors.error.text : theme.colors.success.text;
  }
  return /* @__PURE__ */ jsx("span", { style: { color, fontWeight: "bold" }, children: displayValue });
}

export { DiffCell };
//# sourceMappingURL=DiffCell.mjs.map

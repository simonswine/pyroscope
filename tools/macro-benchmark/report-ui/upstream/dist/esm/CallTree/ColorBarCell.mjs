import { jsx } from 'react/jsx-runtime';
import { css } from '@emotion/css';
import { useStyles2 } from '@grafana/ui';
import { getRowBarColor } from './utils.mjs';

"use strict";
function ColorBarCell({
  node,
  data,
  colorScheme,
  theme,
  focusedNode
}) {
  const styles = useStyles2(getStyles);
  const barColor = getRowBarColor(node, data, colorScheme, theme);
  let barWidth;
  if (focusedNode) {
    if (node.id === focusedNode.parentId) {
      barWidth = "0%";
    } else {
      const relativePercent = focusedNode.total > 0 ? node.total / focusedNode.total * 100 : 0;
      barWidth = `${Math.min(relativePercent, 100)}%`;
    }
  } else {
    barWidth = `${Math.min(node.totalPercent, 100)}%`;
  }
  return /* @__PURE__ */ jsx("div", { className: styles.colorBarContainer, children: /* @__PURE__ */ jsx("div", { className: styles.colorBar, style: { width: barWidth, backgroundColor: barColor } }) });
}
function getStyles(theme) {
  return {
    colorBarContainer: css({
      width: "100%",
      height: "20px",
      display: "flex",
      alignItems: "center"
    }),
    colorBar: css({
      height: "16px",
      minWidth: "2px",
      borderRadius: theme.shape.radius.default
    })
  };
}

export { ColorBarCell };
//# sourceMappingURL=ColorBarCell.mjs.map

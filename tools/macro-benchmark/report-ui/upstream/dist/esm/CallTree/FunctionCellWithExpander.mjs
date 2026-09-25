import { jsxs, jsx } from 'react/jsx-runtime';
import { css } from '@emotion/css';
import { useStyles2, Button } from '@grafana/ui';

"use strict";
function FunctionCellWithExpander({
  row,
  value,
  depth,
  hasChildren,
  rowIndex,
  rows,
  onSymbolClick,
  compact = false,
  toggleRowExpanded
}) {
  const styles = useStyles2(getStyles);
  const expandSingleChildChain = (node) => {
    var _a;
    if (((_a = node.children) == null ? void 0 : _a.length) === 1) {
      const childNode = node.children[0];
      toggleRowExpanded([childNode.id], true);
      if (childNode.children && childNode.children.length > 0) {
        expandSingleChildChain(childNode);
      }
    }
  };
  const handleClick = () => {
    if (hasChildren) {
      const wasExpanded = row.isExpanded;
      row.toggleRowExpanded();
      if (!wasExpanded) {
        expandSingleChildChain(row.original);
      }
    }
    onSymbolClick(value);
  };
  const isLastVisibleChildAtIndex = (index) => {
    if (index === void 0) {
      return false;
    }
    const currentRow = rows[index];
    const parentId = currentRow.original.parentId;
    for (let i = index + 1; i < rows.length; i++) {
      if (rows[i].original.parentId === parentId) {
        return false;
      }
      if (rows[i].original.depth <= currentRow.original.depth) {
        break;
      }
    }
    return true;
  };
  const buildTreeConnector = () => {
    if (depth === 0) {
      return null;
    }
    const lines = [];
    const nodeIdToNode = /* @__PURE__ */ new Map();
    rows.forEach((r) => {
      nodeIdToNode.set(r.original.id, r.original);
    });
    const hasMoreNodesAtDepth = (targetDepth) => {
      if (rowIndex === void 0) {
        return false;
      }
      for (let i = rowIndex + 1; i < rows.length; i++) {
        const checkRow = rows[i];
        if (checkRow.original.depth === targetDepth) {
          return true;
        }
        if (checkRow.original.depth < targetDepth) {
          break;
        }
      }
      return false;
    };
    let currentNode = row.original;
    while (currentNode.parentId && currentNode.depth > 0) {
      const parent = nodeIdToNode.get(currentNode.parentId);
      if (parent) {
        currentNode = parent;
      } else {
        break;
      }
    }
    for (let i = 0; i < depth - 1; i++) {
      if (hasMoreNodesAtDepth(i + 1)) {
        lines.push("\u2502 ");
      } else {
        lines.push("  ");
      }
    }
    const isLastChild = rowIndex !== void 0 ? isLastVisibleChildAtIndex(rowIndex) : false;
    lines.push(isLastChild ? "\u2514\u2500" : "\u251C\u2500");
    return lines.join("");
  };
  const connector = buildTreeConnector();
  return /* @__PURE__ */ jsxs("div", { className: styles.functionCellContainer, children: [
    connector && /* @__PURE__ */ jsxs("span", { className: styles.treeConnector, children: [
      connector,
      " "
    ] }),
    /* @__PURE__ */ jsxs("span", { className: styles.functionNameWrapper, children: [
      /* @__PURE__ */ jsx(Button, { fill: "text", size: "sm", onClick: handleClick, className: styles.functionButton, children: value }),
      !compact && row.original.children && row.original.children.length > 0 && /* @__PURE__ */ jsxs("span", { className: styles.nodeBadge, children: [
        row.original.children.length,
        " ",
        row.original.children.length === 1 ? "child" : "children",
        ",",
        " ",
        row.original.subtreeSize,
        " ",
        row.original.subtreeSize === 1 ? "node" : "nodes"
      ] })
    ] })
  ] });
}
function getStyles(theme) {
  return {
    functionCellContainer: css({
      display: "flex",
      alignItems: "center",
      gap: "2px",
      height: "20px",
      lineHeight: "1",
      overflow: "hidden",
      minWidth: 0
    }),
    treeConnector: css({
      color: theme.colors.text.secondary,
      fontSize: "16px",
      lineHeight: "1",
      fontFamily: "monospace",
      whiteSpace: "pre",
      display: "inline-block",
      verticalAlign: "middle",
      flexShrink: 0
    }),
    functionNameWrapper: css({
      display: "inline-flex",
      alignItems: "center",
      overflow: "hidden",
      minWidth: 0
    }),
    functionButton: css({
      padding: 0,
      fontSize: theme.typography.fontSize,
      textAlign: "left",
      overflow: "hidden",
      textOverflow: "ellipsis",
      whiteSpace: "nowrap",
      minWidth: 0,
      flexShrink: 1
    }),
    nodeBadge: css({
      marginLeft: theme.spacing(0.5),
      fontSize: theme.typography.bodySmall.fontSize,
      color: theme.colors.text.secondary,
      whiteSpace: "nowrap",
      flexShrink: 0
    })
  };
}

export { FunctionCellWithExpander };
//# sourceMappingURL=FunctionCellWithExpander.mjs.map

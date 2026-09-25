import { jsxs, jsx } from 'react/jsx-runtime';
import { cx, css } from '@emotion/css';
import { useEffect } from 'react';
import { useStyles2, Icon } from '@grafana/ui';

"use strict";
function CallTreeTable({
  width,
  height,
  compactModeThreshold,
  isCompact,
  setIsCompact,
  getFunctionColumnWidth,
  getTableProps,
  getTableBodyProps,
  headerGroups,
  rows,
  prepareRow,
  currentSearchMatchId,
  searchMatchRowRef,
  scrollContainerRef,
  focusedNodeId,
  callersNodeLabel
}) {
  const styles = useStyles2(getStyles);
  const SCROLLBAR_WIDTH = 16;
  const availableWidth = width - SCROLLBAR_WIDTH;
  const shouldBeCompact = availableWidth > 0 && availableWidth < compactModeThreshold;
  useEffect(() => {
    if (availableWidth <= 0) {
      return;
    }
    if (shouldBeCompact !== isCompact) {
      setIsCompact(shouldBeCompact);
    }
  }, [availableWidth, shouldBeCompact, isCompact, setIsCompact]);
  const functionColumnWidth = getFunctionColumnWidth(availableWidth, isCompact);
  if (width < 3 || height < 3) {
    return null;
  }
  return /* @__PURE__ */ jsxs("div", { style: { width, height, display: "flex", flexDirection: "column" }, children: [
    /* @__PURE__ */ jsx("table", { ...getTableProps(), className: styles.table, style: { flexShrink: 0 }, children: /* @__PURE__ */ jsx("thead", { className: styles.thead, children: headerGroups.map((headerGroup) => {
      const { key, ...headerGroupProps } = headerGroup.getHeaderGroupProps();
      return /* @__PURE__ */ jsx("tr", { ...headerGroupProps, children: headerGroup.headers.map((column) => {
        const { key: headerKey, ...headerProps } = column.getHeaderProps(column.getSortByToggleProps());
        const columnWidth = column.id === "label" ? functionColumnWidth : column.width;
        return /* @__PURE__ */ jsxs(
          "th",
          {
            ...headerProps,
            className: styles.th,
            style: {
              ...columnWidth !== void 0 && { width: columnWidth },
              textAlign: column.id === "self" || column.id === "total" ? "right" : void 0,
              ...column.minWidth !== void 0 && { minWidth: column.minWidth }
            },
            children: [
              column.render("Header"),
              column.isSorted && /* @__PURE__ */ jsx(
                Icon,
                {
                  name: column.isSortedDesc ? "arrow-down" : "arrow-up",
                  size: "lg",
                  className: styles.sortIcon
                }
              )
            ]
          },
          headerKey
        );
      }) }, key);
    }) }) }),
    /* @__PURE__ */ jsx(
      "div",
      {
        ref: scrollContainerRef,
        style: { flex: 1, overflowY: "scroll", overflowX: "auto" },
        className: styles.scrollContainer,
        children: /* @__PURE__ */ jsx("table", { ...getTableProps(), className: styles.table, children: /* @__PURE__ */ jsx("tbody", { ...getTableBodyProps(), className: styles.tbody, children: rows.map((row, rowIndex) => {
          prepareRow(row);
          const { key, ...rowProps } = row.getRowProps();
          const isFocusedRow = row.original.id === focusedNodeId;
          const isCallersTargetRow = callersNodeLabel && row.original.label === callersNodeLabel;
          const isSearchMatchRow = currentSearchMatchId && row.original.id === currentSearchMatchId;
          return /* @__PURE__ */ jsx(
            "tr",
            {
              ...rowProps,
              ref: isSearchMatchRow ? searchMatchRowRef : null,
              className: cx(
                styles.tr,
                (isFocusedRow || (focusedNodeId == null ? void 0 : focusedNodeId.startsWith("label:")) && focusedNodeId.substring(6) === row.original.label) && styles.focusedRow,
                isCallersTargetRow && styles.callersTargetRow,
                isSearchMatchRow && styles.searchMatchRow
              ),
              children: row.cells.map((cell) => {
                const { key: cellKey, ...cellProps } = cell.getCellProps();
                const isValueColumn = cell.column.id === "self" || cell.column.id === "total";
                const isActionsColumn = cell.column.id === "actions";
                const columnWidth = cell.column.id === "label" ? functionColumnWidth : cell.column.width;
                return /* @__PURE__ */ jsx(
                  "td",
                  {
                    ...cellProps,
                    className: cx(
                      styles.td,
                      isActionsColumn && styles.actionsColumnCell,
                      isValueColumn && styles.valueColumnCell
                    ),
                    style: {
                      ...columnWidth !== void 0 && { width: columnWidth },
                      ...cell.column.minWidth !== void 0 && { minWidth: cell.column.minWidth }
                    },
                    children: cell.render("Cell", { rowIndex })
                  },
                  cellKey
                );
              })
            },
            key
          );
        }) }) })
      }
    )
  ] });
}
function getStyles(theme) {
  return {
    scrollContainer: css({
      "&::-webkit-scrollbar": {
        width: "8px"
      },
      "&::-webkit-scrollbar-track": {
        background: theme.colors.background.secondary
      },
      "&::-webkit-scrollbar-thumb": {
        background: theme.colors.text.disabled,
        borderRadius: theme.shape.radius.default
      },
      "&::-webkit-scrollbar-thumb:hover": {
        background: theme.colors.text.secondary
      }
    }),
    table: css({
      width: "100%",
      tableLayout: "fixed",
      borderCollapse: "collapse",
      fontSize: theme.typography.fontSize,
      color: theme.colors.text.primary
    }),
    thead: css({
      backgroundColor: theme.colors.background.secondary
    }),
    th: css({
      padding: "4px 6px",
      height: "36px",
      textAlign: "left",
      fontWeight: theme.typography.fontWeightMedium,
      borderBottom: `1px solid ${theme.colors.border.weak}`,
      cursor: "pointer",
      userSelect: "none",
      "&:hover": {
        backgroundColor: theme.colors.emphasize(theme.colors.background.secondary, 0.03)
      }
    }),
    tbody: css({
      backgroundColor: theme.colors.background.primary
    }),
    tr: css({
      "&:hover": {
        backgroundColor: theme.colors.emphasize(theme.colors.background.primary, 0.03)
      }
    }),
    focusedRow: css({
      backgroundColor: theme.colors.emphasize(theme.colors.background.primary, 0.08),
      borderLeft: `3px solid ${theme.colors.primary.main}`,
      fontWeight: theme.typography.fontWeightMedium,
      "&:hover": {
        backgroundColor: theme.colors.emphasize(theme.colors.background.primary, 0.1)
      }
    }),
    callersTargetRow: css({
      backgroundColor: theme.colors.emphasize(theme.colors.background.primary, 0.08),
      borderLeft: `3px solid ${theme.colors.info.main}`,
      fontWeight: theme.typography.fontWeightMedium,
      "&:hover": {
        backgroundColor: theme.colors.emphasize(theme.colors.background.primary, 0.1)
      }
    }),
    searchMatchRow: css({
      backgroundColor: theme.colors.warning.transparent,
      borderLeft: `3px solid ${theme.colors.warning.main}`,
      fontWeight: theme.typography.fontWeightMedium,
      "&:hover": {
        backgroundColor: theme.colors.emphasize(theme.colors.warning.transparent, 0.1)
      }
    }),
    td: css({
      padding: "0px 6px",
      borderBottom: "none",
      height: "20px",
      verticalAlign: "middle",
      overflow: "hidden"
    }),
    sortIcon: css({
      marginLeft: theme.spacing(0.5)
    }),
    actionsColumnCell: css({
      backgroundColor: theme.colors.background.secondary,
      "&:hover": {
        backgroundColor: theme.colors.background.secondary
      }
    }),
    valueColumnCell: css({
      overflow: "visible",
      textAlign: "right"
    })
  };
}

export { CallTreeTable };
//# sourceMappingURL=CallTreeTable.mjs.map

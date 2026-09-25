import { jsx, jsxs } from 'react/jsx-runtime';
import { css } from '@emotion/css';
import { memo, useState, useRef, useEffect, useCallback, useMemo } from 'react';
import { useTable, useSortBy, useExpanded } from 'react-table';
import AutoSizer from 'react-virtualized-auto-sizer';
import { useStyles2, useTheme2, Button, Tooltip, Icon, IconButton } from '@grafana/ui';
import { ColorSchemeDiff, ColorScheme } from '../types.mjs';
import { ActionsCell } from './ActionsCell.mjs';
import { CallTreeTable } from './CallTreeTable.mjs';
import { ColorBarCell } from './ColorBarCell.mjs';
import { DiffCell } from './DiffCell.mjs';
import { FunctionCellWithExpander } from './FunctionCellWithExpander.mjs';
import { buildAllCallTreeNodes, buildCallersTree, getInitialExpandedState } from './utils.mjs';

"use strict";
function findCallTreeNode(nodes, searchKey, byLabel) {
  for (const node of nodes) {
    if (byLabel ? node.label === searchKey : node.id === searchKey) {
      return node;
    }
    if (node.children) {
      const found = findCallTreeNode(node.children, searchKey, byLabel);
      if (found) {
        return found;
      }
    }
  }
  return void 0;
}
const FlameGraphCallTreeContainer = memo(
  ({
    data,
    onSymbolClick,
    sandwichItem,
    onSandwich,
    search,
    onSearch,
    focusedItemIndexes,
    setFocusedItemIndexes,
    getExtraContextMenuButtons,
    viewMode,
    paneView
  }) => {
    const [isCompact, setIsCompact] = useState(false);
    const styles = useStyles2(getStyles);
    const theme = useTheme2();
    const scrollContainerRef = useRef(null);
    const lastScrolledMatchRef = useRef(void 0);
    const tableInstanceRef = useRef({ rows: [], toggleRowExpanded: () => {
    } });
    const [focusedNodeId, setFocusedNodeId] = useState(void 0);
    const [callersNodeLabel, setCallersNodeLabel] = useState(void 0);
    useEffect(() => {
      if (sandwichItem !== void 0) {
        setCallersNodeLabel(sandwichItem);
        setFocusedNodeId(void 0);
      } else {
        setCallersNodeLabel(void 0);
      }
    }, [sandwichItem]);
    const searchQuery = search;
    const [currentMatchIndex, setCurrentMatchIndex] = useState(0);
    const handleSetFocusMode = useCallback(
      (nodeIdOrLabel, isLabel = false, itemIndexes) => {
        if (nodeIdOrLabel === void 0) {
          setFocusedNodeId(void 0);
          setFocusedItemIndexes == null ? void 0 : setFocusedItemIndexes(void 0);
        } else if (isLabel) {
          setFocusedNodeId(`label:${nodeIdOrLabel}`);
          setFocusedItemIndexes == null ? void 0 : setFocusedItemIndexes(itemIndexes);
        } else {
          setFocusedNodeId(nodeIdOrLabel);
          setFocusedItemIndexes == null ? void 0 : setFocusedItemIndexes(itemIndexes);
        }
        if (nodeIdOrLabel !== void 0) {
          setCallersNodeLabel(void 0);
        }
      },
      [setFocusedItemIndexes]
    );
    const handleSetCallersMode = useCallback(
      (label) => {
        setCallersNodeLabel(label);
        if (label !== void 0) {
          setFocusedNodeId(void 0);
        }
        onSandwich(label);
      },
      [onSandwich]
    );
    const allNodes = useMemo(() => buildAllCallTreeNodes(data), [data]);
    const { nodes, focusedNode, callersNode } = useMemo(() => {
      let nodesToUse = allNodes;
      let focusedNode2;
      let callersTargetNode;
      if (focusedNodeId) {
        const isLabelSearch = focusedNodeId.startsWith("label:");
        const searchKey = isLabelSearch ? focusedNodeId.substring(6) : focusedNodeId;
        focusedNode2 = findCallTreeNode(allNodes, searchKey, isLabelSearch);
        if (focusedNode2) {
          if (focusedNode2.parentId) {
            const parent = findCallTreeNode(allNodes, focusedNode2.parentId, false);
            if (parent) {
              const modifiedParent = {
                ...parent,
                children: [focusedNode2]
              };
              nodesToUse = [modifiedParent];
            } else {
              nodesToUse = [focusedNode2];
            }
          } else {
            nodesToUse = [focusedNode2];
          }
        }
      }
      if (callersNodeLabel) {
        const [callers] = data.getSandwichLevels(callersNodeLabel);
        if (callers.length > 0) {
          nodesToUse = buildCallersTree(callers, data);
          callersTargetNode = nodesToUse.length > 0 ? nodesToUse[0] : void 0;
        } else {
          nodesToUse = [];
          callersTargetNode = void 0;
        }
      }
      return { nodes: nodesToUse, focusedNode: focusedNode2, callersNode: callersTargetNode };
    }, [allNodes, data, focusedNodeId, callersNodeLabel]);
    const resolvedFocusNodeId = useMemo(() => {
      var _a;
      if (!(focusedNodeId == null ? void 0 : focusedNodeId.startsWith("label:"))) {
        return void 0;
      }
      const searchKey = focusedNodeId.substring(6);
      return (_a = findCallTreeNode(allNodes, searchKey, true)) == null ? void 0 : _a.id;
    }, [focusedNodeId, allNodes]);
    useEffect(() => {
      if (!(focusedNodeId == null ? void 0 : focusedNodeId.startsWith("label:")) || !resolvedFocusNodeId) {
        return;
      }
      if (resolvedFocusNodeId !== focusedNodeId) {
        setFocusedNodeId(resolvedFocusNodeId);
      }
    }, [resolvedFocusNodeId, focusedNodeId]);
    const depthOffset = useMemo(() => {
      if (focusedNodeId && nodes.length > 0) {
        return nodes[0].depth;
      }
      return 0;
    }, [focusedNodeId, nodes]);
    const { searchNodes, searchError } = useMemo(() => {
      if (!searchQuery.trim()) {
        return { searchNodes: [], searchError: void 0 };
      }
      const MAX_MATCHES = 50;
      const matches = [];
      const regexChars = /[.*+?^${}()|[\]\\]/;
      let isRegexQuery = regexChars.test(searchQuery);
      let searchRegex = null;
      let searchError2;
      if (isRegexQuery) {
        try {
          searchRegex = new RegExp(searchQuery, "i");
        } catch (e) {
          searchError2 = "Invalid regex pattern";
          return { searchNodes: [], searchError: searchError2 };
        }
      }
      const searchFn = (nodesToSearch) => {
        for (const node of nodesToSearch) {
          if (matches.length >= MAX_MATCHES) {
            break;
          }
          let isMatch = false;
          if (searchRegex) {
            isMatch = searchRegex.test(node.label);
          } else {
            isMatch = node.label.toLowerCase().includes(searchQuery.toLowerCase());
          }
          if (isMatch) {
            matches.push({ id: node.id, total: node.total });
          }
          if (node.children && matches.length < MAX_MATCHES) {
            searchFn(node.children);
          }
        }
      };
      searchFn(nodes);
      matches.sort((a, b) => b.total - a.total);
      const matchIds = matches.map((m) => m.id);
      return { searchNodes: matchIds, searchError: searchError2 };
    }, [searchQuery, nodes]);
    useEffect(() => {
      if (!focusedItemIndexes || focusedItemIndexes.length === 0) {
        setFocusedNodeId(void 0);
        return;
      }
      const itemIndexesMatch = (a, b) => {
        if (a.length !== b.length) {
          return false;
        }
        return a.every((val, idx) => val === b[idx]);
      };
      const findExactMatch = (nodesToSearch) => {
        for (const node of nodesToSearch) {
          if (itemIndexesMatch(node.levelItem.itemIndexes, focusedItemIndexes)) {
            return node.id;
          }
          if (node.children) {
            const found = findExactMatch(node.children);
            if (found) {
              return found;
            }
          }
        }
        return void 0;
      };
      const matchedNodeId = findExactMatch(allNodes);
      if (matchedNodeId) {
        setFocusedNodeId(matchedNodeId);
      }
    }, [focusedItemIndexes, allNodes]);
    const searchResultKey = searchNodes.join(",");
    useEffect(() => {
      setCurrentMatchIndex(searchNodes.length > 0 ? 0 : -1);
    }, [searchResultKey]);
    const navigateToNextMatch = () => {
      if (searchNodes.length > 0) {
        setCurrentMatchIndex((prev) => (prev + 1) % searchNodes.length);
      }
    };
    const navigateToPrevMatch = () => {
      if (searchNodes.length > 0) {
        setCurrentMatchIndex((prev) => (prev - 1 + searchNodes.length) % searchNodes.length);
      }
    };
    const currentSearchMatchId = useMemo(() => {
      if (searchNodes.length > 0 && currentMatchIndex >= 0 && currentMatchIndex < searchNodes.length) {
        return searchNodes[currentMatchIndex];
      }
      return void 0;
    }, [searchNodes, currentMatchIndex]);
    const searchMatchRowRef = useCallback(
      (node) => {
        if (node && currentSearchMatchId && currentSearchMatchId !== lastScrolledMatchRef.current) {
          lastScrolledMatchRef.current = currentSearchMatchId;
          const container = scrollContainerRef.current;
          if (container) {
            requestAnimationFrame(() => {
              const rowRect = node.getBoundingClientRect();
              const containerRect = container.getBoundingClientRect();
              const rowTopRelativeToContainer = rowRect.top - containerRect.top + container.scrollTop;
              const targetScrollTop = rowTopRelativeToContainer - container.clientHeight / 2 + rowRect.height / 2;
              container.scrollTo({
                top: Math.max(0, targetScrollTop),
                behavior: "smooth"
              });
            });
          }
        }
      },
      [currentSearchMatchId]
    );
    const expandedState = useMemo(() => {
      const baseExpanded = getInitialExpandedState(nodes, 1);
      const expandPathToNode = (nodes2, targetId) => {
        for (const node of nodes2) {
          if (node.id === targetId) {
            return true;
          }
          if (node.children && node.children.length > 0) {
            const foundInSubtree = expandPathToNode(node.children, targetId);
            if (foundInSubtree) {
              baseExpanded[node.id] = true;
              return true;
            }
          }
        }
        return false;
      };
      if (currentSearchMatchId) {
        expandPathToNode(nodes, currentSearchMatchId);
      }
      if (focusedNodeId && nodes.length > 0) {
        const rootNode = nodes[0];
        const isLabelSearch = focusedNodeId.startsWith("label:");
        const searchLabel = isLabelSearch ? focusedNodeId.substring(6) : void 0;
        if (rootNode.children && rootNode.children.length > 0) {
          baseExpanded["0"] = true;
        }
        const isRootTheFocusedNode = isLabelSearch ? rootNode.label === searchLabel : rootNode.id === focusedNodeId;
        if (!isRootTheFocusedNode && rootNode.children && rootNode.children.length > 0) {
          baseExpanded["0.0"] = true;
        }
      }
      if (callersNodeLabel && callersNode && nodes.length > 0) {
        expandPathToNode(nodes, callersNode.id);
        if (callersNode.children && callersNode.children.length > 0) {
          baseExpanded[callersNode.id] = true;
        }
      }
      return baseExpanded;
    }, [nodes, focusedNodeId, callersNodeLabel, callersNode, currentSearchMatchId]);
    const ACTIONS_WIDTH = 30;
    const COLOR_BAR_WIDTH = 200;
    const SELF_WIDTH = 150;
    const TOTAL_WIDTH = 150;
    const BASELINE_WIDTH = 100;
    const COMPARISON_WIDTH = 100;
    const DIFF_WIDTH = 100;
    const FUNCTION_MIN_WIDTH = 100;
    const FUNCTION_COMPACT_THRESHOLD = 550;
    const getFixedColumnsWidth = (isDiff2, compactMode) => {
      if (compactMode) {
        return isDiff2 ? ACTIONS_WIDTH + BASELINE_WIDTH + COMPARISON_WIDTH + DIFF_WIDTH : ACTIONS_WIDTH + TOTAL_WIDTH;
      }
      return isDiff2 ? ACTIONS_WIDTH + COLOR_BAR_WIDTH + BASELINE_WIDTH + COMPARISON_WIDTH + DIFF_WIDTH : ACTIONS_WIDTH + COLOR_BAR_WIDTH + SELF_WIDTH + TOTAL_WIDTH;
    };
    const isDiff = data.isDiffFlamegraph();
    const compactModeThreshold = getFixedColumnsWidth(isDiff, false) + FUNCTION_COMPACT_THRESHOLD;
    const getFunctionColumnWidth = (availableWidth, compactMode) => {
      if (availableWidth <= 0) {
        return void 0;
      }
      const fixedWidth = getFixedColumnsWidth(isDiff, compactMode);
      return Math.max(availableWidth - fixedWidth, FUNCTION_MIN_WIDTH);
    };
    const commonColumns = useMemo(() => {
      return [
        {
          Header: "",
          id: "actions",
          Cell: ({ row }) => {
            var _a, _b;
            return /* @__PURE__ */ jsx(
              ActionsCell,
              {
                nodeId: row.original.id,
                label: row.original.label,
                itemIndexes: row.original.levelItem.itemIndexes,
                levelItem: row.original.levelItem,
                hasChildren: Boolean((_a = row.original.children) == null ? void 0 : _a.length),
                depth: row.original.depth - depthOffset,
                parentId: row.original.parentId,
                onFocus: handleSetFocusMode,
                onShowCallers: handleSetCallersMode,
                onSearch,
                focusedNodeId,
                callersNodeLabel,
                isSearchMatch: (_b = searchNodes == null ? void 0 : searchNodes.includes(row.original.id)) != null ? _b : false,
                actionsCellClass: styles.actionsCell,
                getExtraContextMenuButtons,
                data,
                viewMode,
                paneView,
                search
              }
            );
          },
          width: ACTIONS_WIDTH,
          minWidth: ACTIONS_WIDTH,
          disableSortBy: true
        },
        {
          Header: "Function",
          accessor: "label",
          Cell: ({ row, value, rowIndex }) => {
            var _a;
            return /* @__PURE__ */ jsx(
              FunctionCellWithExpander,
              {
                row,
                value,
                depth: row.original.depth - depthOffset,
                hasChildren: Boolean((_a = row.original.children) == null ? void 0 : _a.length),
                rowIndex,
                rows: tableInstanceRef.current.rows,
                onSymbolClick,
                compact: isCompact,
                toggleRowExpanded: tableInstanceRef.current.toggleRowExpanded
              }
            );
          },
          minWidth: FUNCTION_MIN_WIDTH
        }
      ];
    }, [
      callersNodeLabel,
      data,
      depthOffset,
      focusedNodeId,
      getExtraContextMenuButtons,
      handleSetCallersMode,
      handleSetFocusMode,
      isCompact,
      onSearch,
      onSymbolClick,
      paneView,
      search,
      searchNodes,
      styles,
      viewMode
    ]);
    const columns = useMemo(() => {
      if (data.isDiffFlamegraph()) {
        const cols = [...commonColumns];
        if (!isCompact) {
          cols.push({
            Header: "",
            id: "colorBar",
            Cell: ({ row }) => /* @__PURE__ */ jsx(
              ColorBarCell,
              {
                node: row.original,
                data,
                colorScheme: ColorSchemeDiff.Default,
                theme,
                focusedNode
              }
            ),
            minWidth: COLOR_BAR_WIDTH,
            width: COLOR_BAR_WIDTH,
            disableSortBy: true
          });
        }
        cols.push(
          {
            Header: "Baseline",
            accessor: "totalPercent",
            Cell: ({ value }) => `${value.toFixed(2)}%`,
            sortType: "basic",
            width: BASELINE_WIDTH,
            minWidth: BASELINE_WIDTH
          },
          {
            Header: "Comparison",
            accessor: "totalPercentRight",
            Cell: ({ value }) => value !== void 0 ? `${value.toFixed(2)}%` : "-",
            sortType: "basic",
            width: COMPARISON_WIDTH,
            minWidth: COMPARISON_WIDTH
          },
          {
            Header: "Diff %",
            accessor: "diffPercent",
            Cell: ({ value }) => /* @__PURE__ */ jsx(DiffCell, { value, theme }),
            sortType: "basic",
            width: DIFF_WIDTH,
            minWidth: DIFF_WIDTH
          }
        );
        return cols;
      } else {
        const cols = [...commonColumns];
        if (!isCompact) {
          cols.push(
            {
              Header: "",
              id: "colorBar",
              Cell: ({ row }) => /* @__PURE__ */ jsx(
                ColorBarCell,
                {
                  node: row.original,
                  data,
                  colorScheme: ColorScheme.PackageBased,
                  theme,
                  focusedNode
                }
              ),
              minWidth: COLOR_BAR_WIDTH,
              width: COLOR_BAR_WIDTH,
              disableSortBy: true
            },
            {
              Header: "Self",
              accessor: "self",
              Cell: ({ row }) => {
                const displaySelf = data.valueDisplayProcessor(row.original.self);
                const formattedValue = displaySelf.suffix ? displaySelf.text + displaySelf.suffix : displaySelf.text;
                return /* @__PURE__ */ jsxs("div", { className: styles.valueCell, children: [
                  /* @__PURE__ */ jsx("span", { className: styles.valueNumber, children: formattedValue }),
                  /* @__PURE__ */ jsxs("span", { className: styles.percentNumber, children: [
                    row.original.selfPercent.toFixed(2),
                    "%"
                  ] })
                ] });
              },
              sortType: "basic",
              minWidth: SELF_WIDTH,
              width: SELF_WIDTH
            }
          );
        }
        cols.push({
          Header: "Total",
          accessor: "total",
          Cell: ({ row }) => {
            const displayValue = data.valueDisplayProcessor(row.original.total);
            const formattedValue = displayValue.suffix ? displayValue.text + displayValue.suffix : displayValue.text;
            return /* @__PURE__ */ jsxs("div", { className: styles.valueCell, children: [
              /* @__PURE__ */ jsx("span", { className: styles.valueNumber, children: formattedValue }),
              /* @__PURE__ */ jsxs("span", { className: styles.percentNumber, children: [
                row.original.totalPercent.toFixed(2),
                "%"
              ] })
            ] });
          },
          sortType: "basic",
          minWidth: TOTAL_WIDTH,
          width: TOTAL_WIDTH
        });
        return cols;
      }
    }, [commonColumns, data, isCompact, theme, styles, focusedNode]);
    const tableNodes = useMemo(() => {
      return [...nodes];
    }, [nodes, currentSearchMatchId]);
    const tableInstance = useTable(
      {
        columns,
        data: tableNodes,
        getSubRows: (row) => row.children || [],
        initialState: {
          sortBy: [{ id: "total", desc: true }],
          expanded: expandedState
        },
        autoResetExpanded: true,
        autoResetSortBy: false
      },
      useSortBy,
      useExpanded
    );
    tableInstanceRef.current = tableInstance;
    const { getTableProps, getTableBodyProps, headerGroups, rows, prepareRow } = tableInstance;
    return /* @__PURE__ */ jsxs("div", { className: styles.container, "data-testid": "callTree", children: [
      /* @__PURE__ */ jsx("div", { className: styles.toolbar, children: /* @__PURE__ */ jsxs("div", { className: styles.toolbarLeft, children: [
        searchQuery && /* @__PURE__ */ jsxs("div", { className: styles.searchContainer, children: [
          searchNodes.length > 0 && /* @__PURE__ */ jsxs("div", { className: styles.searchNavigation, children: [
            /* @__PURE__ */ jsxs("span", { className: styles.searchCounter, children: [
              currentMatchIndex + 1,
              " of ",
              searchNodes.length,
              searchNodes.length >= 50 && "+"
            ] }),
            /* @__PURE__ */ jsx(
              Button,
              {
                icon: "angle-up",
                fill: "text",
                size: "sm",
                onClick: navigateToPrevMatch,
                tooltip: "Previous match",
                "aria-label": "Previous match"
              }
            ),
            /* @__PURE__ */ jsx(
              Button,
              {
                icon: "angle-down",
                fill: "text",
                size: "sm",
                onClick: navigateToNextMatch,
                tooltip: "Next match",
                "aria-label": "Next match"
              }
            )
          ] }),
          searchQuery && searchNodes.length === 0 && !searchError && /* @__PURE__ */ jsx("span", { className: styles.searchNoResults, children: "No matches found" }),
          searchError && /* @__PURE__ */ jsx("span", { className: styles.searchError, children: searchError })
        ] }),
        focusedNode && /* @__PURE__ */ jsx(Tooltip, { content: focusedNode.label, placement: "top", children: /* @__PURE__ */ jsxs("div", { className: styles.focusedItem, children: [
          /* @__PURE__ */ jsx(Icon, { size: "sm", name: "compress-arrows" }),
          /* @__PURE__ */ jsx("span", { className: styles.focusedItemLabel, children: focusedNode.label.substring(focusedNode.label.lastIndexOf("/") + 1) }),
          /* @__PURE__ */ jsx(
            IconButton,
            {
              className: styles.modePillCloseButton,
              name: "times",
              size: "sm",
              onClick: () => handleSetFocusMode(void 0),
              tooltip: "Clear callees view",
              "aria-label": "Clear callees view"
            }
          )
        ] }) }),
        callersNode && /* @__PURE__ */ jsx(Tooltip, { content: callersNodeLabel || "", placement: "top", children: /* @__PURE__ */ jsxs("div", { className: styles.callersItem, children: [
          /* @__PURE__ */ jsx(Icon, { size: "sm", name: "expand-arrows-alt" }),
          /* @__PURE__ */ jsx("span", { className: styles.callersItemLabel, children: (callersNodeLabel || "").substring((callersNodeLabel || "").lastIndexOf("/") + 1) }),
          /* @__PURE__ */ jsx(
            IconButton,
            {
              className: styles.modePillCloseButton,
              name: "times",
              size: "sm",
              onClick: () => handleSetCallersMode(void 0),
              tooltip: "Clear callers view",
              "aria-label": "Clear callers view"
            }
          )
        ] }) })
      ] }) }),
      /* @__PURE__ */ jsx("div", { style: { flex: 1, minHeight: 0, overflow: "hidden" }, children: /* @__PURE__ */ jsx(AutoSizer, { children: ({ width, height }) => /* @__PURE__ */ jsx(
        CallTreeTable,
        {
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
        }
      ) }) })
    ] });
  }
);
FlameGraphCallTreeContainer.displayName = "FlameGraphCallTreeContainer";
function getStyles(theme) {
  return {
    container: css({
      width: "100%",
      height: "100%",
      backgroundColor: theme.colors.background.primary,
      display: "flex",
      flexDirection: "column"
    }),
    toolbar: css({
      display: "flex",
      alignItems: "center",
      paddingTop: theme.spacing(1),
      paddingBottom: theme.spacing(1),
      gap: theme.spacing(1),
      flexWrap: "wrap",
      borderBottom: `1px solid ${theme.colors.border.weak}`,
      "&:not(:has(> :not(:empty)))": {
        display: "none"
      }
    }),
    toolbarLeft: css({
      display: "flex",
      alignItems: "center",
      gap: theme.spacing(1)
    }),
    searchContainer: css({
      display: "flex",
      alignItems: "center",
      gap: theme.spacing(1),
      flexWrap: "wrap"
    }),
    searchNavigation: css({
      display: "flex",
      alignItems: "center",
      gap: theme.spacing(0.5),
      padding: `0 ${theme.spacing(1)}`
    }),
    searchCounter: css({
      fontSize: theme.typography.bodySmall.fontSize,
      color: theme.colors.text.secondary,
      whiteSpace: "nowrap"
    }),
    searchNoResults: css({
      fontSize: theme.typography.bodySmall.fontSize,
      color: theme.colors.text.secondary,
      fontStyle: "italic"
    }),
    searchError: css({
      fontSize: theme.typography.bodySmall.fontSize,
      color: theme.colors.error.text
    }),
    actionsCell: css({
      display: "flex",
      alignItems: "center",
      justifyContent: "center",
      height: "20px"
    }),
    valueCell: css({
      display: "flex",
      alignItems: "center",
      justifyContent: "space-between",
      gap: "8px",
      fontVariantNumeric: "tabular-nums",
      height: "20px"
    }),
    valueNumber: css({
      flex: "1 1 auto",
      textAlign: "right",
      whiteSpace: "nowrap",
      minWidth: "60px"
    }),
    percentNumber: css({
      flex: "0 0 60px",
      width: "60px",
      textAlign: "right",
      color: theme.colors.text.secondary,
      whiteSpace: "nowrap"
    }),
    focusedItem: css({
      display: "inline-flex",
      alignItems: "center",
      background: theme.colors.background.secondary,
      borderRadius: theme.shape.radius.default,
      padding: theme.spacing(0.5, 1),
      fontSize: theme.typography.bodySmall.fontSize,
      fontWeight: theme.typography.fontWeightMedium,
      lineHeight: theme.typography.bodySmall.lineHeight,
      color: theme.colors.text.secondary
    }),
    focusedItemLabel: css({
      maxWidth: "200px",
      overflow: "hidden",
      textOverflow: "ellipsis",
      whiteSpace: "nowrap",
      marginLeft: theme.spacing(0.5)
    }),
    callersItem: css({
      display: "inline-flex",
      alignItems: "center",
      background: theme.colors.background.secondary,
      borderRadius: theme.shape.radius.default,
      padding: theme.spacing(0.5, 1),
      fontSize: theme.typography.bodySmall.fontSize,
      fontWeight: theme.typography.fontWeightMedium,
      lineHeight: theme.typography.bodySmall.lineHeight,
      color: theme.colors.text.secondary
    }),
    callersItemLabel: css({
      maxWidth: "200px",
      overflow: "hidden",
      textOverflow: "ellipsis",
      whiteSpace: "nowrap",
      marginLeft: theme.spacing(0.5)
    }),
    modePillCloseButton: css({
      verticalAlign: "text-bottom",
      margin: theme.spacing(0, 0.5)
    })
  };
}

export { FlameGraphCallTreeContainer as default };
//# sourceMappingURL=FlameGraphCallTreeContainer.mjs.map

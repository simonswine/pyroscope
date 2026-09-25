import { jsx, jsxs } from 'react/jsx-runtime';
import { useMemo, useState, useRef, useEffect, useCallback, createElement } from 'react';
import { css } from '@emotion/css';
import uFuzzy from '@leeoniya/ufuzzy';
import { useMeasure, usePrevious } from 'react-use';
import { escapeStringForRegex } from '@grafana/data';
import { ThemeContext } from '@grafana/ui';
import FlameGraph from './FlameGraph/FlameGraph.mjs';
import { CollapsedMap, FlameGraphDataContainer } from './FlameGraph/dataTransform.mjs';
import FlameGraphHeader from './FlameGraphHeader.mjs';
import FlameGraphPane from './FlameGraphPane.mjs';
import FlameGraphTopTableContainer from './TopTable/FlameGraphTopTableContainer.mjs';
import { MIN_WIDTH_TO_SHOW_BOTH_TOPTABLE_AND_FLAMEGRAPH, MIN_WIDTH_FOR_SPLIT_VIEW, FLAMEGRAPH_CONTAINER_HEIGHT } from './constants.mjs';
import { useColorScheme } from './hooks.mjs';
import { SelectedView, ViewMode, PaneView } from './types.mjs';
import { getAssistantContextFromDataFrame } from './utils.mjs';

"use strict";
const ufuzzy = new uFuzzy();
const FlameGraphContainer = ({
  data,
  onTableSymbolClick,
  onViewSelected,
  onTextAlignSelected,
  onTableSort,
  getTheme,
  stickyHeader,
  extraHeaderElements,
  vertical,
  showFlameGraphOnly,
  disableCollapsing,
  keepFocusOnDataChange,
  getExtraContextMenuButtons,
  showAnalyzeWithAssistant = true,
  enableNewUI
}) => {
  const theme = useMemo(() => getTheme(), [getTheme]);
  if (enableNewUI) {
    return /* @__PURE__ */ jsx(
      NewUIContainer,
      {
        data,
        onTableSymbolClick,
        onViewSelected,
        onTextAlignSelected,
        onTableSort,
        theme,
        stickyHeader,
        extraHeaderElements,
        vertical,
        showFlameGraphOnly,
        disableCollapsing,
        keepFocusOnDataChange,
        getExtraContextMenuButtons,
        showAnalyzeWithAssistant
      }
    );
  }
  return /* @__PURE__ */ jsx(
    LegacyContainer,
    {
      data,
      onTableSymbolClick,
      onViewSelected,
      onTextAlignSelected,
      onTableSort,
      theme,
      stickyHeader,
      extraHeaderElements,
      vertical,
      showFlameGraphOnly,
      disableCollapsing,
      keepFocusOnDataChange,
      getExtraContextMenuButtons,
      showAnalyzeWithAssistant
    }
  );
};
const LegacyContainer = ({
  data,
  onTableSymbolClick,
  onViewSelected,
  onTextAlignSelected,
  onTableSort,
  theme,
  stickyHeader,
  extraHeaderElements,
  vertical,
  showFlameGraphOnly,
  disableCollapsing,
  keepFocusOnDataChange,
  getExtraContextMenuButtons,
  showAnalyzeWithAssistant
}) => {
  const [focusedItemData, setFocusedItemData] = useState();
  const [rangeMin, setRangeMin] = useState(0);
  const [rangeMax, setRangeMax] = useState(1);
  const [search, setSearch] = useState("");
  const [selectedView, setSelectedView] = useState(SelectedView.Both);
  const [sizeRef, { width: containerWidth }] = useMeasure();
  const [textAlign, setTextAlign] = useState("left");
  const [sandwichItem, setSandwichItem] = useState();
  const [collapsedMap, setCollapsedMap] = useState(new CollapsedMap());
  const onTableSymbolClickRef = useRef(onTableSymbolClick);
  const onTableSortRef = useRef(onTableSort);
  onTableSymbolClickRef.current = onTableSymbolClick;
  onTableSortRef.current = onTableSort;
  const dataContainer = useMemo(() => {
    if (!data) {
      return;
    }
    const container = new FlameGraphDataContainer(data, { collapsing: !disableCollapsing }, theme);
    setCollapsedMap(container.getCollapsedMap());
    return container;
  }, [data, theme, disableCollapsing]);
  const [colorScheme, setColorScheme] = useColorScheme(dataContainer);
  const styles = getStyles(theme);
  const matchedLabels = useLabelSearch(search, dataContainer);
  useEffect(() => {
    if (containerWidth > 0 && containerWidth < MIN_WIDTH_TO_SHOW_BOTH_TOPTABLE_AND_FLAMEGRAPH && selectedView === SelectedView.Both && !vertical) {
      setSelectedView(SelectedView.FlameGraph);
    }
  }, [selectedView, setSelectedView, containerWidth, vertical]);
  const resetFocus = useCallback(() => {
    setFocusedItemData(void 0);
    setRangeMin(0);
    setRangeMax(1);
  }, [setFocusedItemData, setRangeMax, setRangeMin]);
  const resetSandwich = useCallback(() => {
    setSandwichItem(void 0);
  }, [setSandwichItem]);
  useEffect(() => {
    var _a;
    if (!keepFocusOnDataChange) {
      resetFocus();
      resetSandwich();
      return;
    }
    if (dataContainer && focusedItemData) {
      const item = (_a = dataContainer.getNodesWithLabel(focusedItemData.label)) == null ? void 0 : _a[0];
      if (item) {
        setFocusedItemData({ ...focusedItemData, item });
        const levels = dataContainer.getLevels();
        const totalViewTicks = levels.length ? levels[0][0].value : 0;
        setRangeMin(item.start / totalViewTicks);
        setRangeMax((item.start + item.value) / totalViewTicks);
      } else {
        setFocusedItemData({
          ...focusedItemData,
          item: {
            start: 0,
            value: 0,
            itemIndexes: [],
            children: [],
            level: 0
          }
        });
        setRangeMin(0);
        setRangeMax(1);
      }
    }
  }, [dataContainer, keepFocusOnDataChange]);
  const onSymbolClick = useCallback(
    (symbol) => {
      var _a;
      const anchored = `^${escapeStringForRegex(symbol)}$`;
      if (search === anchored) {
        setSearch("");
      } else {
        (_a = onTableSymbolClickRef.current) == null ? void 0 : _a.call(onTableSymbolClickRef, symbol);
        setSearch(anchored);
        resetFocus();
      }
    },
    [setSearch, resetFocus, search]
  );
  const onSearch = useCallback(
    (str) => {
      if (!str) {
        setSearch("");
        return;
      }
      setSearch(`^${escapeStringForRegex(str)}$`);
    },
    [setSearch]
  );
  const onSandwich = useCallback(
    (label) => {
      resetFocus();
      setSandwichItem(label);
    },
    [resetFocus, setSandwichItem]
  );
  const onTableSortStable = useCallback((sort) => {
    var _a;
    (_a = onTableSortRef.current) == null ? void 0 : _a.call(onTableSortRef, sort);
  }, []);
  if (!dataContainer) {
    return null;
  }
  const flameGraph = /* @__PURE__ */ jsx(
    FlameGraph,
    {
      data: dataContainer,
      rangeMin,
      rangeMax,
      matchedLabels,
      setRangeMin,
      setRangeMax,
      onItemFocused: (data2) => setFocusedItemData(data2),
      focusedItemData,
      textAlign,
      sandwichItem,
      onSandwich,
      onFocusPillClick: resetFocus,
      onSandwichPillClick: resetSandwich,
      colorScheme,
      showFlameGraphOnly,
      collapsing: !disableCollapsing,
      getExtraContextMenuButtons,
      selectedView,
      search,
      collapsedMap,
      setCollapsedMap
    }
  );
  const table = /* @__PURE__ */ jsx(
    FlameGraphTopTableContainer,
    {
      data: dataContainer,
      onSymbolClick,
      search,
      matchedLabels,
      sandwichItem,
      onSandwich: setSandwichItem,
      onSearch,
      onTableSort: onTableSortStable,
      colorScheme
    }
  );
  let body;
  if (showFlameGraphOnly || selectedView === SelectedView.FlameGraph) {
    body = flameGraph;
  } else if (selectedView === SelectedView.TopTable) {
    body = /* @__PURE__ */ jsx("div", { className: styles.tableContainer, children: table });
  } else if (selectedView === SelectedView.Both) {
    if (vertical) {
      body = /* @__PURE__ */ jsxs("div", { children: [
        /* @__PURE__ */ jsx("div", { className: styles.verticalGraphContainer, children: flameGraph }),
        /* @__PURE__ */ jsx("div", { className: styles.verticalTableContainer, children: table })
      ] });
    } else {
      body = /* @__PURE__ */ jsxs("div", { className: styles.horizontalContainer, children: [
        /* @__PURE__ */ jsx("div", { className: styles.horizontalTableContainer, children: table }),
        /* @__PURE__ */ jsx("div", { className: styles.horizontalGraphContainer, children: flameGraph })
      ] });
    }
  }
  return (
    // We add the theme context to bridge the gap if this is rendered in non grafana environment where the context
    // isn't already provided.
    /* @__PURE__ */ jsx(ThemeContext.Provider, { value: theme, children: /* @__PURE__ */ jsxs("div", { ref: sizeRef, className: styles.container, children: [
      !showFlameGraphOnly && /* @__PURE__ */ jsx(
        FlameGraphHeader,
        {
          search,
          setSearch,
          selectedView,
          setSelectedView: (view) => {
            setSelectedView(view);
            onViewSelected == null ? void 0 : onViewSelected(view);
          },
          containerWidth,
          onReset: () => {
            resetFocus();
            resetSandwich();
          },
          textAlign,
          onTextAlignChange: (align) => {
            setTextAlign(align);
            onTextAlignSelected == null ? void 0 : onTextAlignSelected(align);
          },
          showResetButton: Boolean(focusedItemData || sandwichItem),
          colorScheme,
          onColorSchemeChange: setColorScheme,
          stickyHeader: Boolean(stickyHeader),
          extraHeaderElements,
          vertical,
          isDiffMode: dataContainer.isDiffFlamegraph(),
          setCollapsedMap,
          collapsedMap,
          assistantContext: data && showAnalyzeWithAssistant ? getAssistantContextFromDataFrame(data) : void 0
        }
      ),
      /* @__PURE__ */ jsx("div", { className: styles.body, children: body })
    ] }) })
  );
};
const NewUIContainer = ({
  data,
  onTableSymbolClick,
  onViewSelected,
  onTextAlignSelected,
  onTableSort,
  theme,
  stickyHeader,
  extraHeaderElements,
  vertical,
  showFlameGraphOnly,
  disableCollapsing,
  keepFocusOnDataChange,
  getExtraContextMenuButtons,
  showAnalyzeWithAssistant
}) => {
  const [search, setSearch] = useState("");
  const [viewMode, setViewMode] = useState(ViewMode.Split);
  const [leftPaneView, setLeftPaneView] = useState(PaneView.TopTable);
  const [rightPaneView, setRightPaneView] = useState(PaneView.FlameGraph);
  const [singleView, setSingleView] = useState(PaneView.FlameGraph);
  const [panesSwapped, setPanesSwapped] = useState(false);
  const [sizeRef, { width: containerWidth }] = useMeasure();
  const [resetKey, setResetKey] = useState(0);
  const [focusedItemIndexes, setFocusedItemIndexes] = useState(void 0);
  const [sharedSandwichItem, setSharedSandwichItem] = useState(void 0);
  const canShowSplitView = containerWidth > 0 && (containerWidth >= MIN_WIDTH_FOR_SPLIT_VIEW || Boolean(vertical));
  const onTableSymbolClickRef = useRef(onTableSymbolClick);
  const onTextAlignSelectedRef = useRef(onTextAlignSelected);
  const onTableSortRef = useRef(onTableSort);
  useEffect(() => {
    onTableSymbolClickRef.current = onTableSymbolClick;
    onTextAlignSelectedRef.current = onTextAlignSelected;
    onTableSortRef.current = onTableSort;
  });
  const stableOnTableSymbolClick = useCallback((symbol) => {
    var _a;
    (_a = onTableSymbolClickRef.current) == null ? void 0 : _a.call(onTableSymbolClickRef, symbol);
  }, []);
  const stableOnTextAlignSelected = useCallback((align) => {
    var _a;
    (_a = onTextAlignSelectedRef.current) == null ? void 0 : _a.call(onTextAlignSelectedRef, align);
  }, []);
  const stableOnTableSort = useCallback((sort) => {
    var _a;
    (_a = onTableSortRef.current) == null ? void 0 : _a.call(onTableSortRef, sort);
  }, []);
  const dataContainer = useMemo(() => {
    if (!data) {
      return;
    }
    return new FlameGraphDataContainer(data, { collapsing: !disableCollapsing }, theme);
  }, [data, theme, disableCollapsing]);
  const styles = getStyles(theme);
  const matchedLabels = useLabelSearch(search, dataContainer);
  const effectiveViewMode = canShowSplitView ? viewMode : ViewMode.Single;
  const prevViewMode = usePrevious(viewMode);
  useEffect(() => {
    if (prevViewMode === void 0) {
      return;
    }
    if (prevViewMode === ViewMode.Split && viewMode === ViewMode.Single) {
      setSingleView(rightPaneView);
    } else if (prevViewMode === ViewMode.Single && viewMode === ViewMode.Split) {
      setRightPaneView(singleView);
    }
  }, [viewMode, prevViewMode, rightPaneView, singleView]);
  if (!dataContainer) {
    return null;
  }
  const commonPaneProps = {
    dataContainer,
    search,
    matchedLabels,
    onTableSymbolClick: stableOnTableSymbolClick,
    onTextAlignSelected: stableOnTextAlignSelected,
    onTableSort: stableOnTableSort,
    showFlameGraphOnly,
    disableCollapsing,
    getExtraContextMenuButtons,
    setSearch,
    resetKey,
    keepFocusOnDataChange,
    focusedItemIndexes,
    setFocusedItemIndexes
  };
  let body;
  if (showFlameGraphOnly) {
    body = /* @__PURE__ */ jsx(
      FlameGraphPane,
      {
        ...commonPaneProps,
        paneView: PaneView.FlameGraph,
        viewMode: effectiveViewMode,
        paneViewForContextMenu: PaneView.FlameGraph,
        sharedSandwichItem,
        setSharedSandwichItem
      }
    );
  } else if (effectiveViewMode === ViewMode.Single) {
    body = /* @__PURE__ */ jsx(
      FlameGraphPane,
      {
        ...commonPaneProps,
        paneView: singleView,
        viewMode: ViewMode.Single,
        paneViewForContextMenu: singleView,
        sharedSandwichItem,
        setSharedSandwichItem
      }
    );
  } else {
    const shouldSyncSandwich = leftPaneView !== rightPaneView;
    const leftPane = /* @__PURE__ */ createElement(
      FlameGraphPane,
      {
        ...commonPaneProps,
        key: "left-pane",
        paneView: leftPaneView,
        viewMode: ViewMode.Split,
        paneViewForContextMenu: leftPaneView,
        sharedSandwichItem: shouldSyncSandwich ? sharedSandwichItem : void 0,
        setSharedSandwichItem: shouldSyncSandwich ? setSharedSandwichItem : void 0
      }
    );
    const rightPane = /* @__PURE__ */ createElement(
      FlameGraphPane,
      {
        ...commonPaneProps,
        key: "right-pane",
        paneView: rightPaneView,
        viewMode: ViewMode.Split,
        paneViewForContextMenu: rightPaneView,
        sharedSandwichItem: shouldSyncSandwich ? sharedSandwichItem : void 0,
        setSharedSandwichItem: shouldSyncSandwich ? setSharedSandwichItem : void 0
      }
    );
    if (vertical) {
      body = /* @__PURE__ */ jsxs("div", { className: styles.verticalContainer, children: [
        /* @__PURE__ */ jsx("div", { className: styles.verticalPaneContainer, style: { order: panesSwapped ? 2 : 1 }, children: leftPane }),
        /* @__PURE__ */ jsx("div", { className: styles.verticalPaneContainer, style: { order: panesSwapped ? 1 : 2 }, children: rightPane })
      ] });
    } else {
      body = /* @__PURE__ */ jsxs("div", { className: styles.horizontalContainer, children: [
        /* @__PURE__ */ jsx("div", { className: styles.horizontalPaneContainer, style: { order: panesSwapped ? 2 : 1 }, children: leftPane }),
        /* @__PURE__ */ jsx("div", { className: styles.horizontalPaneContainer, style: { order: panesSwapped ? 1 : 2 }, children: rightPane })
      ] });
    }
  }
  return /* @__PURE__ */ jsx(ThemeContext.Provider, { value: theme, children: /* @__PURE__ */ jsxs("div", { ref: sizeRef, className: styles.container, children: [
    !showFlameGraphOnly && /* @__PURE__ */ jsx(
      FlameGraphHeader,
      {
        enableNewUI: true,
        search,
        setSearch,
        viewMode,
        setViewMode: (mode) => {
          setViewMode(mode);
          onViewSelected == null ? void 0 : onViewSelected(mode === ViewMode.Split ? "split" : singleView);
        },
        canShowSplitView,
        containerWidth,
        leftPaneView: panesSwapped ? rightPaneView : leftPaneView,
        setLeftPaneView: panesSwapped ? setRightPaneView : setLeftPaneView,
        rightPaneView: panesSwapped ? leftPaneView : rightPaneView,
        setRightPaneView: panesSwapped ? setLeftPaneView : setRightPaneView,
        singleView,
        setSingleView: (view) => {
          setSingleView(view);
          if (viewMode === ViewMode.Single) {
            onViewSelected == null ? void 0 : onViewSelected(view);
          }
        },
        onSwapPanes: () => setPanesSwapped((s) => !s),
        onReset: () => {
          setSearch("");
          setFocusedItemIndexes(void 0);
          setSharedSandwichItem(void 0);
          setResetKey((k) => k + 1);
        },
        showResetButton: Boolean(search),
        stickyHeader: Boolean(stickyHeader),
        extraHeaderElements,
        assistantContext: data && showAnalyzeWithAssistant ? getAssistantContextFromDataFrame(data) : void 0
      }
    ),
    /* @__PURE__ */ jsx("div", { className: styles.body, children: body })
  ] }) });
};
function useLabelSearch(search, data) {
  return useMemo(() => {
    if (!search || !data) {
      return void 0;
    }
    return labelSearch(search, data);
  }, [search, data]);
}
function labelSearch(search, data) {
  const foundLabels = /* @__PURE__ */ new Set();
  const terms = search.split(",");
  const regexFilter = (labels, pattern) => {
    let regex;
    try {
      regex = new RegExp(pattern);
    } catch (e) {
      return false;
    }
    let foundMatch = false;
    for (let label of labels) {
      if (!regex.test(label)) {
        continue;
      }
      foundLabels.add(label);
      foundMatch = true;
    }
    return foundMatch;
  };
  const fuzzyFilter = (labels, term) => {
    let idxs = ufuzzy.filter(labels, term);
    if (!idxs) {
      return false;
    }
    let foundMatch = false;
    for (let idx of idxs) {
      foundLabels.add(labels[idx]);
      foundMatch = true;
    }
    return foundMatch;
  };
  for (let term of terms) {
    if (!term) {
      continue;
    }
    const found = regexFilter(data.getUniqueLabels(), term);
    if (!found) {
      fuzzyFilter(data.getUniqueLabels(), term);
    }
  }
  return foundLabels;
}
function getStyles(theme) {
  return {
    container: css({
      label: "container",
      overflow: "auto",
      height: "100%",
      display: "flex",
      flex: "1 1 0",
      flexDirection: "column",
      minHeight: 0,
      gap: theme.spacing(1)
    }),
    body: css({
      label: "body",
      flexGrow: 1
    }),
    tableContainer: css({
      // This is not ideal for dashboard panel where it creates a double scroll. In a panel it should be 100% but then
      // in explore we need a specific height.
      height: FLAMEGRAPH_CONTAINER_HEIGHT
    }),
    horizontalContainer: css({
      label: "horizontalContainer",
      display: "flex",
      minHeight: 0,
      flexDirection: "row",
      columnGap: theme.spacing(1),
      width: "100%"
    }),
    horizontalGraphContainer: css({
      flexBasis: "50%",
      minWidth: 0
    }),
    horizontalTableContainer: css({
      flexBasis: "50%",
      minWidth: 0,
      maxHeight: FLAMEGRAPH_CONTAINER_HEIGHT,
      overflow: "auto"
    }),
    verticalGraphContainer: css({
      marginBottom: theme.spacing(1)
    }),
    verticalTableContainer: css({
      height: FLAMEGRAPH_CONTAINER_HEIGHT
    }),
    verticalContainer: css({
      label: "verticalContainer",
      display: "flex",
      flexDirection: "column"
    }),
    horizontalPaneContainer: css({
      label: "horizontalPaneContainer",
      flexBasis: "50%",
      maxHeight: FLAMEGRAPH_CONTAINER_HEIGHT,
      minWidth: 0,
      overflow: "auto"
    }),
    verticalPaneContainer: css({
      label: "verticalPaneContainer",
      marginBottom: theme.spacing(1),
      height: FLAMEGRAPH_CONTAINER_HEIGHT
    })
  };
}

export { FlameGraphContainer as default, labelSearch };
//# sourceMappingURL=FlameGraphContainer.mjs.map

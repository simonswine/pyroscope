import { jsx } from 'react/jsx-runtime';
import { css } from '@emotion/css';
import { useState, useCallback, useLayoutEffect, useEffect, useRef } from 'react';
import { escapeStringForRegex } from '@grafana/data';
import FlameGraphCallTreeContainer from './CallTree/FlameGraphCallTreeContainer.mjs';
import FlameGraph from './FlameGraph/FlameGraph.mjs';
import FlameGraphTopTableContainer from './TopTable/FlameGraphTopTableContainer.mjs';
import { FLAMEGRAPH_CONTAINER_HEIGHT } from './constants.mjs';
import { useColorScheme } from './hooks.mjs';
import { PaneView } from './types.mjs';

"use strict";
const FlameGraphPane = ({
  paneView,
  dataContainer,
  search,
  matchedLabels,
  onTableSymbolClick,
  onTextAlignSelected,
  onTableSort,
  showFlameGraphOnly,
  disableCollapsing,
  getExtraContextMenuButtons,
  viewMode,
  paneViewForContextMenu,
  setSearch,
  resetKey,
  keepFocusOnDataChange,
  focusedItemIndexes,
  setFocusedItemIndexes,
  sharedSandwichItem,
  setSharedSandwichItem
}) => {
  const [focusedItemData, setFocusedItemData] = useState();
  const [rangeMin, setRangeMin] = useState(0);
  const [rangeMax, setRangeMax] = useState(1);
  const [textAlign, setTextAlign] = useState("left");
  const [localSandwichItem, setLocalSandwichItem] = useState();
  const isUsingSharedSandwich = setSharedSandwichItem !== void 0;
  const sandwichItem = isUsingSharedSandwich ? sharedSandwichItem : localSandwichItem;
  const setSandwichItem = useCallback(
    (item) => {
      if (isUsingSharedSandwich && setSharedSandwichItem) {
        setSharedSandwichItem(item);
      } else {
        setLocalSandwichItem(item);
      }
    },
    [isUsingSharedSandwich, setSharedSandwichItem]
  );
  const [collapsedMap, setCollapsedMap] = useState(() => dataContainer.getCollapsedMap());
  const [colorScheme, setColorScheme] = useColorScheme(dataContainer);
  const styles = getStyles();
  useLayoutEffect(() => {
    setCollapsedMap(dataContainer.getCollapsedMap());
  }, [dataContainer]);
  useEffect(() => {
    if (resetKey !== void 0 && resetKey > 0) {
      setFocusedItemData(void 0);
      setRangeMin(0);
      setRangeMax(1);
      setLocalSandwichItem(void 0);
    }
  }, [resetKey]);
  useEffect(() => {
    var _a;
    if (!keepFocusOnDataChange) {
      setFocusedItemData(void 0);
      setRangeMin(0);
      setRangeMax(1);
      setSandwichItem(void 0);
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
  const weSetFocusRef = useRef(false);
  useEffect(() => {
    if (!focusedItemIndexes || focusedItemIndexes.length === 0) {
      return;
    }
    if (weSetFocusRef.current) {
      weSetFocusRef.current = false;
      return;
    }
    const currentIndexes = focusedItemData == null ? void 0 : focusedItemData.item.itemIndexes;
    if (currentIndexes && currentIndexes.length === focusedItemIndexes.length) {
      const matches = currentIndexes.every((val, idx) => val === focusedItemIndexes[idx]);
      if (matches) {
        return;
      }
    }
    const levels = dataContainer.getLevels();
    for (const level of levels) {
      for (const item of level) {
        if (item.itemIndexes.length === focusedItemIndexes.length && item.itemIndexes.every((val, idx) => val === focusedItemIndexes[idx])) {
          const label = dataContainer.getLabel(item.itemIndexes[0]);
          const totalViewTicks = levels[0][0].value;
          setFocusedItemData({ label, item, posX: 0, posY: 0 });
          setRangeMin(item.start / totalViewTicks);
          setRangeMax((item.start + item.value) / totalViewTicks);
          return;
        }
      }
    }
  }, [focusedItemIndexes, dataContainer, focusedItemData]);
  const resetFocus = useCallback(() => {
    setFocusedItemData(void 0);
    setRangeMin(0);
    setRangeMax(1);
    setFocusedItemIndexes == null ? void 0 : setFocusedItemIndexes(void 0);
  }, [setFocusedItemIndexes]);
  const resetSandwich = useCallback(() => {
    setSandwichItem(void 0);
  }, [setSandwichItem]);
  const onSymbolClick = useCallback(
    (symbol) => {
      const anchored = `^${escapeStringForRegex(symbol)}$`;
      if (search === anchored) {
        setSearch("");
      } else {
        onTableSymbolClick == null ? void 0 : onTableSymbolClick(symbol);
        setSearch(anchored);
        resetFocus();
      }
    },
    [search, setSearch, resetFocus, onTableSymbolClick]
  );
  const onCallTreeSymbolClick = useCallback(
    (symbol) => {
      onTableSymbolClick == null ? void 0 : onTableSymbolClick(symbol);
    },
    [onTableSymbolClick]
  );
  const onCallTreeSearch = useCallback(
    (symbol) => {
      const anchored = `^${escapeStringForRegex(symbol)}$`;
      if (search === anchored) {
        setSearch("");
      } else {
        onTableSymbolClick == null ? void 0 : onTableSymbolClick(symbol);
        setSearch(anchored);
        resetFocus();
      }
    },
    [search, setSearch, resetFocus, onTableSymbolClick]
  );
  const onTopTableSearch = useCallback(
    (str) => {
      if (!str) {
        setSearch("");
        return;
      }
      setSearch(`^${escapeStringForRegex(str)}$`);
    },
    [setSearch]
  );
  let content;
  switch (paneView) {
    case PaneView.TopTable:
      content = /* @__PURE__ */ jsx("div", { className: styles.tableContainer, children: /* @__PURE__ */ jsx(
        FlameGraphTopTableContainer,
        {
          data: dataContainer,
          onSymbolClick,
          search,
          matchedLabels,
          sandwichItem,
          onSandwich: setSandwichItem,
          onSearch: onTopTableSearch,
          onTableSort,
          colorScheme
        }
      ) });
      break;
    case PaneView.FlameGraph:
    default:
      content = /* @__PURE__ */ jsx(
        FlameGraph,
        {
          data: dataContainer,
          rangeMin,
          rangeMax,
          matchedLabels,
          setRangeMin,
          setRangeMax,
          onItemFocused: (data) => {
            setFocusedItemData(data);
            weSetFocusRef.current = true;
            setFocusedItemIndexes == null ? void 0 : setFocusedItemIndexes(data.item.itemIndexes);
          },
          focusedItemData,
          textAlign,
          onTextAlignChange: (align) => {
            setTextAlign(align);
            onTextAlignSelected == null ? void 0 : onTextAlignSelected(align);
          },
          sandwichItem,
          onSandwich: (label) => {
            resetFocus();
            setSandwichItem(label);
          },
          onFocusPillClick: resetFocus,
          onSandwichPillClick: resetSandwich,
          colorScheme,
          onColorSchemeChange: setColorScheme,
          isDiffMode: dataContainer.isDiffFlamegraph(),
          showFlameGraphOnly,
          collapsing: !disableCollapsing,
          getExtraContextMenuButtons,
          enableNewUI: true,
          viewMode,
          paneView: paneViewForContextMenu,
          search,
          collapsedMap,
          setCollapsedMap
        }
      );
      break;
    case PaneView.CallTree:
      content = /* @__PURE__ */ jsx("div", { className: styles.tableContainer, children: /* @__PURE__ */ jsx(
        FlameGraphCallTreeContainer,
        {
          data: dataContainer,
          onSymbolClick: onCallTreeSymbolClick,
          sandwichItem,
          onSandwich: setSandwichItem,
          onTableSort,
          search,
          onSearch: onCallTreeSearch,
          focusedItemIndexes,
          setFocusedItemIndexes,
          getExtraContextMenuButtons,
          viewMode,
          paneView: paneViewForContextMenu
        }
      ) });
      break;
  }
  return /* @__PURE__ */ jsx("div", { className: styles.paneWrapper, children: content });
};
function getStyles() {
  return {
    paneWrapper: css({
      width: "100%",
      height: "100%"
    }),
    tableContainer: css({
      height: FLAMEGRAPH_CONTAINER_HEIGHT,
      minWidth: 0,
      overflow: "hidden"
    })
  };
}

export { FlameGraphPane as default };
//# sourceMappingURL=FlameGraphPane.mjs.map

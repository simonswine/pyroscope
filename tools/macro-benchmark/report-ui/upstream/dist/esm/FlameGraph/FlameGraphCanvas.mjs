import { jsxs, jsx } from 'react/jsx-runtime';
import { css } from '@emotion/css';
import { useRef, useState, useCallback, useEffect } from 'react';
import { useMeasure } from 'react-use';
import { PIXELS_PER_LEVEL } from '../constants.mjs';
import FlameGraphContextMenu from './FlameGraphContextMenu.mjs';
import FlameGraphTooltip from './FlameGraphTooltip.mjs';
import { useFlameRender, getBarX } from './rendering.mjs';

"use strict";
const FlameGraphCanvas = ({
  data,
  rangeMin,
  rangeMax,
  matchedLabels,
  setRangeMin,
  setRangeMax,
  onItemFocused,
  focusedItemData,
  textAlign,
  onSandwich,
  colorScheme,
  totalProfileTicks,
  totalProfileTicksRight,
  totalViewTicks,
  root,
  direction,
  depth,
  showFlameGraphOnly,
  collapsedMap,
  setCollapsedMap,
  collapsing,
  getExtraContextMenuButtons,
  selectedView,
  viewMode,
  paneView,
  search
}) => {
  const styles = getStyles();
  const [sizeRef, { width: wrapperWidth }] = useMeasure();
  const graphRef = useRef(null);
  const [tooltipItem, setTooltipItem] = useState();
  const [clickedItemData, setClickedItemData] = useState();
  useFlameRender({
    canvasRef: graphRef,
    colorScheme,
    data,
    focusedItemData,
    root,
    direction,
    depth,
    rangeMax,
    rangeMin,
    matchedLabels,
    textAlign,
    totalViewTicks,
    // We need this so that if we have a diff profile and are in sandwich view we still show the same diff colors.
    totalColorTicks: data.isDiffFlamegraph() ? totalProfileTicks : totalViewTicks,
    totalTicksRight: totalProfileTicksRight,
    wrapperWidth,
    collapsedMap
  });
  const onGraphClick = useCallback(
    (e) => {
      setTooltipItem(void 0);
      const pixelsPerTick = graphRef.current.clientWidth / totalViewTicks / (rangeMax - rangeMin);
      const item = convertPixelCoordinatesToBarCoordinates(
        { x: e.nativeEvent.offsetX, y: e.nativeEvent.offsetY },
        root,
        direction,
        depth,
        pixelsPerTick,
        totalViewTicks,
        rangeMin,
        collapsedMap
      );
      if (item) {
        setClickedItemData({
          posY: e.clientY,
          posX: e.clientX,
          item,
          label: data.getLabel(item.itemIndexes[0])
        });
      } else {
        setClickedItemData(void 0);
      }
    },
    [data, rangeMin, rangeMax, totalViewTicks, root, direction, depth, collapsedMap]
  );
  const [mousePosition, setMousePosition] = useState();
  const onGraphMouseMove = useCallback(
    (e) => {
      if (clickedItemData === void 0) {
        setTooltipItem(void 0);
        setMousePosition(void 0);
        const pixelsPerTick = graphRef.current.clientWidth / totalViewTicks / (rangeMax - rangeMin);
        const item = convertPixelCoordinatesToBarCoordinates(
          { x: e.nativeEvent.offsetX, y: e.nativeEvent.offsetY },
          root,
          direction,
          depth,
          pixelsPerTick,
          totalViewTicks,
          rangeMin,
          collapsedMap
        );
        if (item) {
          setMousePosition({ x: e.clientX, y: e.clientY });
          setTooltipItem(item);
        }
      }
    },
    [rangeMin, rangeMax, totalViewTicks, clickedItemData, setMousePosition, root, direction, depth, collapsedMap]
  );
  const onGraphMouseLeave = useCallback(() => {
    setTooltipItem(void 0);
  }, []);
  useEffect(() => {
    const handleOnClick = (e) => {
      var _a;
      if (e.target instanceof HTMLElement && ((_a = e.target.parentElement) == null ? void 0 : _a.id) !== "flameGraphCanvasContainer_clickOutsideCheck") {
        setClickedItemData(void 0);
      }
    };
    window.addEventListener("click", handleOnClick);
    return () => window.removeEventListener("click", handleOnClick);
  }, [setClickedItemData]);
  return /* @__PURE__ */ jsxs("div", { className: styles.graph, children: [
    /* @__PURE__ */ jsx("div", { className: styles.canvasWrapper, id: "flameGraphCanvasContainer_clickOutsideCheck", ref: sizeRef, children: /* @__PURE__ */ jsx(
      "canvas",
      {
        ref: graphRef,
        "data-testid": "flameGraph",
        onClick: onGraphClick,
        onMouseMove: onGraphMouseMove,
        onMouseLeave: onGraphMouseLeave
      }
    ) }),
    /* @__PURE__ */ jsx(
      FlameGraphTooltip,
      {
        position: mousePosition,
        item: tooltipItem,
        data,
        totalTicks: totalViewTicks,
        collapseConfig: tooltipItem ? collapsedMap.get(tooltipItem) : void 0
      }
    ),
    !showFlameGraphOnly && clickedItemData && /* @__PURE__ */ jsx(
      FlameGraphContextMenu,
      {
        data,
        itemData: clickedItemData,
        collapsing,
        collapseConfig: collapsedMap.get(clickedItemData.item),
        onMenuItemClick: () => {
          setClickedItemData(void 0);
        },
        onItemFocus: () => {
          setRangeMin(clickedItemData.item.start / totalViewTicks);
          setRangeMax((clickedItemData.item.start + clickedItemData.item.value) / totalViewTicks);
          onItemFocused(clickedItemData);
        },
        onSandwich: () => {
          onSandwich(data.getLabel(clickedItemData.item.itemIndexes[0]));
        },
        onExpandGroup: () => {
          setCollapsedMap(collapsedMap.setCollapsedStatus(clickedItemData.item, false));
        },
        onCollapseGroup: () => {
          setCollapsedMap(collapsedMap.setCollapsedStatus(clickedItemData.item, true));
        },
        onExpandAllGroups: () => {
          setCollapsedMap(collapsedMap.setAllCollapsedStatus(false));
        },
        onCollapseAllGroups: () => {
          setCollapsedMap(collapsedMap.setAllCollapsedStatus(true));
        },
        allGroupsCollapsed: Array.from(collapsedMap.values()).every((i) => i.collapsed),
        allGroupsExpanded: Array.from(collapsedMap.values()).every((i) => !i.collapsed),
        getExtraContextMenuButtons,
        selectedView,
        viewMode,
        paneView,
        search
      }
    )
  ] });
};
const getStyles = () => ({
  graph: css({
    label: "graph",
    overflow: "auto",
    flexGrow: 1,
    flexBasis: "50%"
  }),
  canvasContainer: css({
    label: "canvasContainer",
    display: "flex"
  }),
  canvasWrapper: css({
    label: "canvasWrapper",
    cursor: "pointer",
    flex: 1,
    overflow: "hidden"
  }),
  sandwichMarker: css({
    label: "sandwichMarker",
    writingMode: "vertical-lr",
    transform: "rotate(180deg)",
    overflow: "hidden",
    whiteSpace: "nowrap"
  }),
  sandwichMarkerIcon: css({
    label: "sandwichMarkerIcon",
    verticalAlign: "baseline"
  })
});
const convertPixelCoordinatesToBarCoordinates = (pos, root, direction, depth, pixelsPerTick, totalTicks, rangeMin, collapsedMap) => {
  let next = root;
  let currentLevel = direction === "children" ? 0 : depth - 1;
  const levelIndex = Math.floor(pos.y / PIXELS_PER_LEVEL);
  let found = void 0;
  while (next) {
    const node = next;
    next = void 0;
    if (currentLevel === levelIndex) {
      found = node;
      break;
    }
    const nextList = direction === "children" ? node.children : node.parents || [];
    for (const child of nextList) {
      const xStart = getBarX(child.start, totalTicks, rangeMin, pixelsPerTick);
      const xEnd = getBarX(child.start + child.value, totalTicks, rangeMin, pixelsPerTick);
      if (xStart <= pos.x && pos.x < xEnd) {
        next = child;
        const collapsedConfig = collapsedMap.get(child);
        if (!collapsedConfig || !collapsedConfig.collapsed || collapsedConfig.items[0] === child) {
          currentLevel = currentLevel + (direction === "children" ? 1 : -1);
        }
        break;
      }
    }
  }
  return found;
};

export { convertPixelCoordinatesToBarCoordinates, FlameGraphCanvas as default };
//# sourceMappingURL=FlameGraphCanvas.mjs.map

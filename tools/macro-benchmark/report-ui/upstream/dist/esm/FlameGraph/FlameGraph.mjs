import { jsxs, Fragment, jsx } from 'react/jsx-runtime';
import { cx, css } from '@emotion/css';
import { useState, useEffect } from 'react';
import { useStyles2, Icon, ButtonGroup, Button, RadioButtonGroup } from '@grafana/ui';
import { ColorSchemeButton } from '../ColorSchemeButton.mjs';
import { alignOptions } from '../FlameGraphHeader.mjs';
import { PIXELS_PER_LEVEL } from '../constants.mjs';
import FlameGraphCanvas from './FlameGraphCanvas.mjs';
import FlameGraphMetadata from './FlameGraphMetadata.mjs';

"use strict";
const FlameGraph = ({
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
  sandwichItem,
  onFocusPillClick,
  onSandwichPillClick,
  colorScheme,
  showFlameGraphOnly,
  getExtraContextMenuButtons,
  collapsing,
  search,
  collapsedMap,
  setCollapsedMap,
  selectedView,
  enableNewUI,
  viewMode,
  paneView,
  onTextAlignChange,
  onColorSchemeChange,
  isDiffMode
}) => {
  const isNewUI = enableNewUI === true;
  const newStyles = useStyles2(getStylesNew);
  const legacyStyles = getStylesLegacy();
  const [levels, setLevels] = useState();
  const [levelsCallers, setLevelsCallers] = useState();
  const [totalProfileTicks, setTotalProfileTicks] = useState(0);
  const [totalProfileTicksRight, setTotalProfileTicksRight] = useState();
  const [totalViewTicks, setTotalViewTicks] = useState(0);
  useEffect(() => {
    var _a, _b, _c;
    if (data) {
      let levels2 = data.getLevels();
      let totalProfileTicks2 = levels2.length ? levels2[0][0].value : 0;
      let totalProfileTicksRight2 = levels2.length ? levels2[0][0].valueRight : void 0;
      let totalViewTicks2 = totalProfileTicks2;
      let levelsCallers2 = void 0;
      if (sandwichItem) {
        const [callers, callees] = data.getSandwichLevels(sandwichItem);
        levels2 = callees;
        levelsCallers2 = callers;
        totalViewTicks2 = (_c = (_b = (_a = callees[0]) == null ? void 0 : _a[0]) == null ? void 0 : _b.value) != null ? _c : 0;
      }
      setLevels(levels2);
      setLevelsCallers(levelsCallers2);
      setTotalProfileTicks(totalProfileTicks2);
      setTotalProfileTicksRight(totalProfileTicksRight2);
      setTotalViewTicks(totalViewTicks2);
    }
  }, [data, sandwichItem]);
  if (!levels) {
    return null;
  }
  const commonCanvasProps = {
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
    showFlameGraphOnly,
    collapsedMap,
    setCollapsedMap,
    getExtraContextMenuButtons,
    collapsing,
    search,
    selectedView,
    viewMode,
    paneView
  };
  let canvas = null;
  const canvasStyles = isNewUI ? newStyles : legacyStyles;
  if (levelsCallers == null ? void 0 : levelsCallers.length) {
    canvas = /* @__PURE__ */ jsxs(Fragment, { children: [
      /* @__PURE__ */ jsxs("div", { className: canvasStyles.sandwichCanvasWrapper, children: [
        /* @__PURE__ */ jsxs("div", { className: canvasStyles.sandwichMarker, children: [
          "Callers",
          /* @__PURE__ */ jsx(Icon, { className: canvasStyles.sandwichMarkerIcon, name: "arrow-down" })
        ] }),
        /* @__PURE__ */ jsx(
          FlameGraphCanvas,
          {
            ...commonCanvasProps,
            root: levelsCallers[levelsCallers.length - 1][0],
            depth: levelsCallers.length,
            direction: "parents",
            collapsing: false
          }
        )
      ] }),
      /* @__PURE__ */ jsxs("div", { className: canvasStyles.sandwichCanvasWrapper, children: [
        /* @__PURE__ */ jsxs("div", { className: cx(canvasStyles.sandwichMarker, canvasStyles.sandwichMarkerCalees), children: [
          /* @__PURE__ */ jsx(Icon, { className: canvasStyles.sandwichMarkerIcon, name: "arrow-up" }),
          "Callees"
        ] }),
        /* @__PURE__ */ jsx(
          FlameGraphCanvas,
          {
            ...commonCanvasProps,
            root: levels[0][0],
            depth: levels.length,
            direction: "children",
            collapsing: false
          }
        )
      ] })
    ] });
  } else if (levels == null ? void 0 : levels.length) {
    canvas = /* @__PURE__ */ jsx(FlameGraphCanvas, { ...commonCanvasProps, root: levels[0][0], depth: levels.length, direction: "children" });
  }
  if (isNewUI) {
    return /* @__PURE__ */ jsxs("div", { className: newStyles.graph, children: [
      /* @__PURE__ */ jsxs("div", { className: newStyles.toolbar, children: [
        /* @__PURE__ */ jsx(
          FlameGraphMetadata,
          {
            data,
            focusedItem: focusedItemData,
            sandwichedLabel: sandwichItem,
            totalTicks: totalViewTicks,
            onFocusPillClick,
            onSandwichPillClick
          }
        ),
        /* @__PURE__ */ jsxs("div", { className: newStyles.controls, children: [
          onColorSchemeChange && /* @__PURE__ */ jsx(ColorSchemeButton, { value: colorScheme, onChange: onColorSchemeChange, isDiffMode: isDiffMode != null ? isDiffMode : false }),
          /* @__PURE__ */ jsxs(ButtonGroup, { className: newStyles.buttonSpacing, children: [
            /* @__PURE__ */ jsx(
              Button,
              {
                variant: "secondary",
                fill: "outline",
                size: "sm",
                tooltip: "Expand all groups",
                onClick: () => {
                  setCollapsedMap(collapsedMap.setAllCollapsedStatus(false));
                },
                "aria-label": "Expand all groups",
                icon: "angle-double-down"
              }
            ),
            /* @__PURE__ */ jsx(
              Button,
              {
                variant: "secondary",
                fill: "outline",
                size: "sm",
                tooltip: "Collapse all groups",
                onClick: () => {
                  setCollapsedMap(collapsedMap.setAllCollapsedStatus(true));
                },
                "aria-label": "Collapse all groups",
                icon: "angle-double-up"
              }
            )
          ] }),
          onTextAlignChange && /* @__PURE__ */ jsx(
            RadioButtonGroup,
            {
              size: "sm",
              options: alignOptions,
              value: textAlign,
              onChange: onTextAlignChange
            }
          )
        ] })
      ] }),
      canvas
    ] });
  }
  return /* @__PURE__ */ jsxs("div", { className: legacyStyles.graph, children: [
    /* @__PURE__ */ jsx(
      FlameGraphMetadata,
      {
        data,
        focusedItem: focusedItemData,
        sandwichedLabel: sandwichItem,
        totalTicks: totalViewTicks,
        onFocusPillClick,
        onSandwichPillClick
      }
    ),
    canvas
  ] });
};
const getStylesLegacy = () => ({
  graph: css({
    label: "graph",
    overflow: "auto",
    flexGrow: 1,
    flexBasis: "50%"
  }),
  sandwichCanvasWrapper: css({
    label: "sandwichCanvasWrapper",
    display: "flex",
    marginBottom: `${PIXELS_PER_LEVEL}px`
  }),
  sandwichMarker: css({
    label: "sandwichMarker",
    writingMode: "vertical-lr",
    transform: "rotate(180deg)",
    overflow: "hidden",
    whiteSpace: "nowrap"
  }),
  sandwichMarkerCalees: css({
    label: "sandwichMarkerCalees",
    textAlign: "right"
  }),
  sandwichMarkerIcon: css({
    label: "sandwichMarkerIcon",
    verticalAlign: "baseline"
  })
});
const getStylesNew = (theme) => ({
  graph: css({
    label: "graph",
    overflow: "auto",
    flexGrow: 1,
    flexBasis: "50%"
  }),
  toolbar: css({
    label: "toolbar",
    display: "flex",
    justifyContent: "space-between",
    alignItems: "center"
  }),
  controls: css({
    label: "controls",
    display: "flex",
    alignItems: "center",
    gap: theme.spacing(1)
  }),
  buttonSpacing: css({
    label: "buttonSpacing",
    marginRight: theme.spacing(1)
  }),
  sandwichCanvasWrapper: css({
    label: "sandwichCanvasWrapper",
    display: "flex",
    marginBottom: `${PIXELS_PER_LEVEL}px`
  }),
  sandwichMarker: css({
    label: "sandwichMarker",
    writingMode: "vertical-lr",
    transform: "rotate(180deg)",
    overflow: "hidden",
    whiteSpace: "nowrap"
  }),
  sandwichMarkerCalees: css({
    label: "sandwichMarkerCalees",
    textAlign: "right"
  }),
  sandwichMarkerIcon: css({
    label: "sandwichMarkerIcon",
    verticalAlign: "baseline"
  })
});

export { FlameGraph as default };
//# sourceMappingURL=FlameGraph.mjs.map

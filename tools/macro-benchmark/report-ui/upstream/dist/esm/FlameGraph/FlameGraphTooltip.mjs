import { jsx, jsxs } from 'react/jsx-runtime';
import { css } from '@emotion/css';
import { getValueFormat } from '@grafana/data';
import { useStyles2, InteractiveTable, Portal, VizTooltipContainer } from '@grafana/ui';

"use strict";
const FlameGraphTooltip = ({ data, item, totalTicks, position, collapseConfig }) => {
  const styles = useStyles2(getStyles);
  if (!(item && position)) {
    return null;
  }
  let content;
  if (data.isDiffFlamegraph()) {
    const tableData = getDiffTooltipData(data, item, totalTicks);
    content = /* @__PURE__ */ jsx(
      InteractiveTable,
      {
        className: styles.tooltipTable,
        columns: [
          { id: "label", header: "" },
          { id: "baseline", header: "Baseline" },
          { id: "comparison", header: "Comparison" },
          { id: "diff", header: "Diff" }
        ],
        data: tableData,
        getRowId: (originalRow) => originalRow.rowId
      }
    );
  } else {
    const tooltipData = getTooltipData(data, item, totalTicks);
    content = /* @__PURE__ */ jsxs("p", { className: styles.lastParagraph, children: [
      tooltipData.unitTitle,
      /* @__PURE__ */ jsx("br", {}),
      "Total: ",
      /* @__PURE__ */ jsx("b", { children: tooltipData.unitValue }),
      " (",
      tooltipData.percentValue,
      "%)",
      /* @__PURE__ */ jsx("br", {}),
      "Self: ",
      /* @__PURE__ */ jsx("b", { children: tooltipData.unitSelf }),
      " (",
      tooltipData.percentSelf,
      "%)",
      /* @__PURE__ */ jsx("br", {}),
      "Samples: ",
      /* @__PURE__ */ jsx("b", { children: tooltipData.samples })
    ] });
  }
  return /* @__PURE__ */ jsx(Portal, { children: /* @__PURE__ */ jsx(VizTooltipContainer, { className: styles.tooltipContainer, position, offset: { x: 15, y: 0 }, children: /* @__PURE__ */ jsxs("div", { className: styles.tooltipContent, children: [
    /* @__PURE__ */ jsxs("p", { className: styles.tooltipName, children: [
      data.getLabel(item.itemIndexes[0]),
      collapseConfig && collapseConfig.collapsed ? /* @__PURE__ */ jsxs("span", { children: [
        /* @__PURE__ */ jsx("br", {}),
        "and ",
        collapseConfig.items.length,
        " similar items"
      ] }) : ""
    ] }),
    content
  ] }) }) });
};
const getTooltipData = (data, item, totalTicks) => {
  const displayValue = data.valueDisplayProcessor(item.value);
  const displaySelf = data.getSelfDisplay(item.itemIndexes);
  const percentValue = Math.round(1e4 * (displayValue.numeric / totalTicks)) / 100;
  const percentSelf = Math.round(1e4 * (displaySelf.numeric / totalTicks)) / 100;
  let unitValue = displayValue.text + displayValue.suffix;
  let unitSelf = displaySelf.text + displaySelf.suffix;
  const unitTitle = data.getUnitTitle();
  if (unitTitle === "Count") {
    if (!displayValue.suffix) {
      unitValue = displayValue.text;
    }
    if (!displaySelf.suffix) {
      unitSelf = displaySelf.text;
    }
  }
  return {
    percentValue,
    percentSelf,
    unitTitle,
    unitValue,
    unitSelf,
    samples: displayValue.numeric.toLocaleString()
  };
};
const formatWithSuffix = (value, formatter) => {
  const displayValue = formatter(value);
  return displayValue.text + displayValue.suffix;
};
const getDiffTooltipData = (data, item, totalTicks) => {
  const levels = data.getLevels();
  const totalTicksRight = levels[0][0].valueRight;
  const totalTicksLeft = totalTicks - totalTicksRight;
  const valueLeft = item.value - item.valueRight;
  const percentageLeft = Math.round(1e4 * valueLeft / totalTicksLeft) / 100;
  const percentageRight = Math.round(1e4 * item.valueRight / totalTicksRight) / 100;
  const diff = (percentageRight - percentageLeft) / percentageLeft * 100;
  const displayValueLeft = getValueWithUnit(data, data.valueDisplayProcessor(valueLeft));
  const displayValueRight = getValueWithUnit(data, data.valueDisplayProcessor(item.valueRight));
  const shortValFormat = getValueFormat("short");
  return [
    {
      rowId: "1",
      label: "% of total",
      baseline: percentageLeft + "%",
      comparison: percentageRight + "%",
      diff: formatWithSuffix(diff, shortValFormat) + "%"
    },
    {
      rowId: "2",
      label: "Value",
      baseline: displayValueLeft,
      comparison: displayValueRight,
      diff: getValueWithUnit(data, data.valueDisplayProcessor(item.valueRight - valueLeft))
    },
    {
      rowId: "3",
      label: "Samples",
      baseline: formatWithSuffix(valueLeft, shortValFormat),
      comparison: formatWithSuffix(item.valueRight, shortValFormat),
      diff: formatWithSuffix(item.valueRight - valueLeft, shortValFormat)
    }
  ];
};
function getValueWithUnit(data, displayValue) {
  let unitValue = displayValue.text + displayValue.suffix;
  const unitTitle = data.getUnitTitle();
  if (unitTitle === "Count") {
    if (!displayValue.suffix) {
      unitValue = displayValue.text;
    }
  }
  return unitValue;
}
const getStyles = (theme) => ({
  tooltipContainer: css({
    title: "tooltipContainer",
    overflow: "hidden"
  }),
  tooltipContent: css({
    title: "tooltipContent",
    fontSize: theme.typography.bodySmall.fontSize,
    width: "100%"
  }),
  tooltipName: css({
    title: "tooltipName",
    marginTop: 0,
    wordBreak: "break-all"
  }),
  lastParagraph: css({
    title: "lastParagraph",
    marginBottom: 0
  }),
  name: css({
    title: "name",
    marginBottom: "10px"
  }),
  tooltipTable: css({
    title: "tooltipTable",
    maxWidth: "400px"
  })
});

export { FlameGraphTooltip as default, getDiffTooltipData, getTooltipData };
//# sourceMappingURL=FlameGraphTooltip.mjs.map

import { jsx, jsxs } from 'react/jsx-runtime';
import { css } from '@emotion/css';
import { memo, useMemo, useState } from 'react';
import AutoSizer from 'react-virtualized-auto-sizer';
import { FieldType, MappingType, applyFieldOverrides, escapeStringForRegex } from '@grafana/data';
import { useStyles2, useTheme2, Table, TableCellDisplayMode, IconButton } from '@grafana/ui';
import { diffColorBlindColors, diffDefaultColors } from '../FlameGraph/colors.mjs';
import { TOP_TABLE_COLUMN_WIDTH } from '../constants.mjs';
import { ColorSchemeDiff } from '../types.mjs';

"use strict";
const FlameGraphTopTableContainer = memo(
  ({
    data,
    onSymbolClick,
    search,
    matchedLabels,
    onSearch,
    sandwichItem,
    onSandwich,
    onTableSort,
    colorScheme
  }) => {
    const table = useMemo(() => buildFilteredTable(data, matchedLabels), [data, matchedLabels]);
    const styles = useStyles2(getStyles);
    const theme = useTheme2();
    const [sort, setSort] = useState([{ displayName: "Self", desc: true }]);
    return /* @__PURE__ */ jsx("div", { className: styles.topTableContainer, "data-testid": "topTable", children: /* @__PURE__ */ jsx(AutoSizer, { style: { width: "100%" }, children: ({ width, height }) => {
      if (width < 3 || height < 3) {
        return null;
      }
      const frame = buildTableDataFrame(
        data,
        table,
        width,
        onSymbolClick,
        onSearch,
        onSandwich,
        theme,
        colorScheme,
        search,
        sandwichItem
      );
      return /* @__PURE__ */ jsx(
        Table,
        {
          initialSortBy: sort,
          onSortByChange: (s) => {
            if (s && s.length) {
              onTableSort == null ? void 0 : onTableSort(s[0].displayName + "_" + (s[0].desc ? "desc" : "asc"));
            }
            setSort(s);
          },
          data: frame,
          width,
          height
        }
      );
    } }) });
  }
);
FlameGraphTopTableContainer.displayName = "FlameGraphTopTableContainer";
function buildFilteredTable(data, matchedLabels) {
  let filteredTable = /* @__PURE__ */ Object.create(null);
  const callStack = [];
  for (let i = 0; i < data.data.length; i++) {
    const value = data.getValue(i);
    const valueRight = data.getValueRight(i);
    const self = data.getSelf(i);
    const label = data.getLabel(i);
    const level = data.getLevel(i);
    while (callStack.length > level) {
      callStack.pop();
    }
    const isRecursive = callStack.some((entry) => entry === label);
    if (!matchedLabels || matchedLabels.has(label)) {
      filteredTable[label] = filteredTable[label] || {};
      filteredTable[label].self = filteredTable[label].self ? filteredTable[label].self + self : self;
      if (!isRecursive) {
        filteredTable[label].total = filteredTable[label].total ? filteredTable[label].total + value : value;
        filteredTable[label].totalRight = filteredTable[label].totalRight ? filteredTable[label].totalRight + valueRight : valueRight;
      }
    }
    callStack.push(label);
  }
  return filteredTable;
}
function buildTableDataFrame(data, table, width, onSymbolClick, onSearch, onSandwich, theme, colorScheme, search, sandwichItem) {
  const actionField = createActionField(onSandwich, onSearch, search, sandwichItem);
  const symbolField = {
    type: FieldType.string,
    name: "Symbol",
    values: [],
    config: {
      custom: { width: width - actionColumnWidth - TOP_TABLE_COLUMN_WIDTH * 2 },
      links: [
        {
          title: "Highlight symbol",
          url: "",
          onClick: (e) => {
            const field = e.origin.field;
            const value = field.values[e.origin.rowIndex];
            onSymbolClick(value);
          }
        }
      ]
    }
  };
  let frame;
  if (data.isDiffFlamegraph()) {
    symbolField.config.custom.width = width - actionColumnWidth - TOP_TABLE_COLUMN_WIDTH * 3;
    const baselineField = createNumberField("Baseline", "percent");
    const comparisonField = createNumberField("Comparison", "percent");
    const diffField = createNumberField("Diff", "percent");
    diffField.config.custom.cellOptions.type = TableCellDisplayMode.ColorText;
    const [removeColor, addColor] = colorScheme === ColorSchemeDiff.DiffColorBlind ? [diffColorBlindColors[0], diffColorBlindColors[2]] : [diffDefaultColors[0], diffDefaultColors[2]];
    diffField.config.mappings = [
      { type: MappingType.ValueToText, options: { [Infinity]: { text: "new", color: addColor } } },
      { type: MappingType.ValueToText, options: { [-100]: { text: "removed", color: removeColor } } },
      { type: MappingType.RangeToText, options: { from: 0, to: Infinity, result: { color: addColor } } },
      { type: MappingType.RangeToText, options: { from: -Infinity, to: 0, result: { color: removeColor } } }
    ];
    const levels = data.getLevels();
    const totalTicks = levels.length ? levels[0][0].value : 0;
    const totalTicksRight = levels.length ? levels[0][0].valueRight : void 0;
    for (let key in table) {
      actionField.values.push(null);
      symbolField.values.push(key);
      const ticksLeft = table[key].total;
      const ticksRight = table[key].totalRight;
      const totalTicksLeft = totalTicks - totalTicksRight;
      const percentageLeft = Math.round(1e4 * ticksLeft / totalTicksLeft) / 100;
      const percentageRight = Math.round(1e4 * ticksRight / totalTicksRight) / 100;
      const diff = (percentageRight - percentageLeft) / percentageLeft * 100;
      diffField.values.push(diff);
      baselineField.values.push(percentageLeft);
      comparisonField.values.push(percentageRight);
    }
    frame = {
      fields: [actionField, symbolField, baselineField, comparisonField, diffField],
      length: symbolField.values.length
    };
  } else {
    const selfField = createNumberField("Self", data.selfField.config.unit);
    const totalField = createNumberField("Total", data.valueField.config.unit);
    for (let key in table) {
      actionField.values.push(null);
      symbolField.values.push(key);
      selfField.values.push(table[key].self);
      totalField.values.push(table[key].total);
    }
    frame = { fields: [actionField, symbolField, selfField, totalField], length: symbolField.values.length };
  }
  const dataFrames = applyFieldOverrides({
    data: [frame],
    fieldConfig: {
      defaults: {},
      overrides: []
    },
    replaceVariables: (value) => value,
    theme
  });
  return dataFrames[0];
}
function createNumberField(name, unit) {
  const tableFieldOptions = {
    width: TOP_TABLE_COLUMN_WIDTH,
    align: "auto",
    inspect: false,
    cellOptions: { type: TableCellDisplayMode.Auto }
  };
  return {
    type: FieldType.number,
    name,
    values: [],
    config: {
      unit,
      custom: tableFieldOptions
    }
  };
}
const actionColumnWidth = 61;
function createActionField(onSandwich, onSearch, search, sandwichItem) {
  const options = {
    type: TableCellDisplayMode.Custom,
    cellComponent: (props) => {
      return /* @__PURE__ */ jsx(
        ActionCell,
        {
          frame: props.frame,
          onSandwich,
          onSearch,
          search,
          sandwichItem,
          rowIndex: props.rowIndex
        }
      );
    }
  };
  const actionFieldTableConfig = {
    filterable: false,
    width: actionColumnWidth,
    hideHeader: true,
    inspect: false,
    align: "auto",
    cellOptions: options
  };
  return {
    type: FieldType.number,
    name: "actions",
    values: [],
    config: {
      custom: actionFieldTableConfig
    }
  };
}
function ActionCell(props) {
  var _a;
  const styles = getStylesActionCell();
  const symbol = (_a = props.frame.fields.find((f) => f.name === "Symbol")) == null ? void 0 : _a.values[props.rowIndex];
  const isSearched = props.search === `^${escapeStringForRegex(String(symbol))}$`;
  const isSandwiched = props.sandwichItem === symbol;
  return /* @__PURE__ */ jsxs("div", { className: styles.actionCellWrapper, children: [
    /* @__PURE__ */ jsx(
      IconButton,
      {
        className: styles.actionCellButton,
        name: "search",
        variant: isSearched ? "primary" : "secondary",
        tooltip: isSearched ? "Clear from search" : "Search for symbol",
        "aria-label": isSearched ? "Clear from search" : "Search for symbol",
        onClick: () => {
          props.onSearch(isSearched ? "" : symbol);
        }
      }
    ),
    /* @__PURE__ */ jsx(
      IconButton,
      {
        className: styles.actionCellButton,
        name: "gf-show-context",
        tooltip: isSandwiched ? "Remove from sandwich view" : "Show in sandwich view",
        variant: isSandwiched ? "primary" : "secondary",
        "aria-label": isSandwiched ? "Remove from sandwich view" : "Show in sandwich view",
        onClick: () => {
          props.onSandwich(isSandwiched ? void 0 : symbol);
        }
      }
    )
  ] });
}
const getStyles = (theme) => {
  return {
    topTableContainer: css({
      label: "topTableContainer",
      padding: theme.spacing(1),
      backgroundColor: theme.colors.background.secondary,
      height: "100%"
    })
  };
};
const getStylesActionCell = () => {
  return {
    actionCellWrapper: css({
      label: "actionCellWrapper",
      display: "flex",
      height: "24px"
    }),
    actionCellButton: css({
      label: "actionCellButton",
      marginRight: 0,
      width: "24px"
    })
  };
};

export { buildFilteredTable, FlameGraphTopTableContainer as default };
//# sourceMappingURL=FlameGraphTopTableContainer.mjs.map

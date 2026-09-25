import { jsxs, jsx } from 'react/jsx-runtime';
import { css } from '@emotion/css';
import { memo } from 'react';
import { getValueFormat } from '@grafana/data';
import { useStyles2, Tooltip, Icon, IconButton } from '@grafana/ui';

"use strict";
const FlameGraphMetadata = memo(
  ({ data, focusedItem, totalTicks, sandwichedLabel, onFocusPillClick, onSandwichPillClick }) => {
    const styles = useStyles2(getStyles);
    const parts = [];
    const ticksVal = getValueFormat("short")(totalTicks);
    const displayValue = data.valueDisplayProcessor(totalTicks);
    let unitValue = displayValue.text + displayValue.suffix;
    const unitTitle = data.getUnitTitle();
    if (unitTitle === "Count") {
      if (!displayValue.suffix) {
        unitValue = displayValue.text;
      }
    }
    parts.push(
      /* @__PURE__ */ jsxs("div", { className: styles.metadataPill, children: [
        unitValue,
        " | ",
        ticksVal.text,
        ticksVal.suffix,
        " samples (",
        unitTitle,
        ")"
      ] }, "default")
    );
    if (sandwichedLabel) {
      parts.push(
        /* @__PURE__ */ jsx(Tooltip, { content: sandwichedLabel, placement: "top", children: /* @__PURE__ */ jsxs("div", { children: [
          /* @__PURE__ */ jsx(Icon, { size: "sm", name: "angle-right" }),
          /* @__PURE__ */ jsxs("div", { className: styles.metadataPill, children: [
            /* @__PURE__ */ jsx(Icon, { size: "sm", name: "gf-show-context" }),
            " ",
            /* @__PURE__ */ jsx("span", { className: styles.metadataPillName, children: sandwichedLabel.substring(sandwichedLabel.lastIndexOf("/") + 1) }),
            /* @__PURE__ */ jsx(
              IconButton,
              {
                className: styles.pillCloseButton,
                name: "times",
                size: "sm",
                onClick: onSandwichPillClick,
                tooltip: "Remove sandwich view",
                "aria-label": "Remove sandwich view"
              }
            )
          ] })
        ] }) }, "sandwich")
      );
    }
    if (focusedItem) {
      const percentValue = totalTicks > 0 ? Math.round(1e4 * (focusedItem.item.value / totalTicks)) / 100 : 0;
      const iconName = percentValue > 0 ? "eye" : "exclamation-circle";
      parts.push(
        /* @__PURE__ */ jsx(Tooltip, { content: focusedItem.label, placement: "top", children: /* @__PURE__ */ jsxs("div", { children: [
          /* @__PURE__ */ jsx(Icon, { size: "sm", name: "angle-right" }),
          /* @__PURE__ */ jsxs("div", { className: styles.metadataPill, children: [
            /* @__PURE__ */ jsx(Icon, { size: "sm", name: iconName }),
            "\xA0",
            percentValue,
            "% of total",
            /* @__PURE__ */ jsx(
              IconButton,
              {
                className: styles.pillCloseButton,
                name: "times",
                size: "sm",
                onClick: onFocusPillClick,
                tooltip: "Remove focus",
                "aria-label": "Remove focus"
              }
            )
          ] })
        ] }) }, "focus")
      );
    }
    return /* @__PURE__ */ jsx("div", { className: styles.metadata, children: parts });
  }
);
FlameGraphMetadata.displayName = "FlameGraphMetadata";
const getStyles = (theme) => ({
  metadataPill: css({
    label: "metadataPill",
    display: "inline-flex",
    alignItems: "center",
    background: theme.colors.background.secondary,
    borderRadius: theme.shape.borderRadius(8),
    padding: theme.spacing(0.5, 1),
    fontSize: theme.typography.bodySmall.fontSize,
    fontWeight: theme.typography.fontWeightMedium,
    lineHeight: theme.typography.bodySmall.lineHeight,
    color: theme.colors.text.secondary
  }),
  pillCloseButton: css({
    label: "pillCloseButton",
    verticalAlign: "text-bottom",
    margin: theme.spacing(0, 0.5)
  }),
  metadata: css({
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    margin: "8px 0"
  }),
  metadataPillName: css({
    label: "metadataPillName",
    maxWidth: "200px",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
    marginLeft: theme.spacing(0.5)
  })
});

export { FlameGraphMetadata as default };
//# sourceMappingURL=FlameGraphMetadata.mjs.map

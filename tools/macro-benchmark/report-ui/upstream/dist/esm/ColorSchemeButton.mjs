import { jsxs, jsx } from 'react/jsx-runtime';
import { cx, css } from '@emotion/css';
import { useStyles2, Menu, Dropdown, Button } from '@grafana/ui';
import { diffColorBlindGradient, diffDefaultGradient, byPackageGradient, byValueGradient } from './FlameGraph/colors.mjs';
import { ColorScheme, ColorSchemeDiff } from './types.mjs';

"use strict";
function ColorSchemeButton(props) {
  const styles = useStyles2(getStyles);
  let menu = /* @__PURE__ */ jsxs(Menu, { children: [
    /* @__PURE__ */ jsx(Menu.Item, { label: "By package name", onClick: () => props.onChange(ColorScheme.PackageBased) }),
    /* @__PURE__ */ jsx(Menu.Item, { label: "By value", onClick: () => props.onChange(ColorScheme.ValueBased) })
  ] });
  const colorDotStyle = {
    [ColorScheme.ValueBased]: styles.colorDotByValue,
    [ColorScheme.PackageBased]: styles.colorDotByPackage,
    [ColorSchemeDiff.DiffColorBlind]: styles.colorDotDiffColorBlind,
    [ColorSchemeDiff.Default]: styles.colorDotDiffDefault
  }[props.value] || styles.colorDotByValue;
  let contents = /* @__PURE__ */ jsx("span", { className: cx(styles.colorDot, colorDotStyle) });
  if (props.isDiffMode) {
    menu = /* @__PURE__ */ jsxs(Menu, { children: [
      /* @__PURE__ */ jsx(Menu.Item, { label: "Default (green to red)", onClick: () => props.onChange(ColorSchemeDiff.Default) }),
      /* @__PURE__ */ jsx(Menu.Item, { label: "Color blind (blue to red)", onClick: () => props.onChange(ColorSchemeDiff.DiffColorBlind) })
    ] });
    contents = /* @__PURE__ */ jsxs("div", { className: cx(styles.colorDotDiff, colorDotStyle), children: [
      /* @__PURE__ */ jsx("div", { children: "-100% (removed)" }),
      /* @__PURE__ */ jsx("div", { children: "0%" }),
      /* @__PURE__ */ jsx("div", { children: "+100% (added)" })
    ] });
  }
  return /* @__PURE__ */ jsx(Dropdown, { overlay: menu, children: /* @__PURE__ */ jsx(
    Button,
    {
      variant: "secondary",
      fill: "outline",
      size: "sm",
      tooltip: "Change color scheme",
      onClick: () => {
      },
      className: styles.buttonSpacing,
      "aria-label": "Change color scheme",
      children: contents
    }
  ) });
}
const getStyles = (theme) => ({
  buttonSpacing: css({
    label: "buttonSpacing",
    marginRight: theme.spacing(1)
  }),
  colorDot: css({
    label: "colorDot",
    display: "inline-block",
    width: "10px",
    height: "10px",
    borderRadius: theme.shape.radius.circle
  }),
  colorDotDiff: css({
    label: "colorDotDiff",
    display: "flex",
    width: "200px",
    height: "12px",
    color: "white",
    fontSize: 9,
    lineHeight: 1.3,
    fontWeight: 300,
    justifyContent: "space-between",
    padding: "0 2px",
    // eslint-disable-next-line @grafana/no-border-radius-literal
    borderRadius: "2px"
  }),
  colorDotByValue: css({
    label: "colorDotByValue",
    background: byValueGradient
  }),
  colorDotByPackage: css({
    label: "colorDotByPackage",
    background: byPackageGradient
  }),
  colorDotDiffDefault: css({
    label: "colorDotDiffDefault",
    background: diffDefaultGradient
  }),
  colorDotDiffColorBlind: css({
    label: "colorDotDiffColorBlind",
    background: diffColorBlindGradient
  })
});

export { ColorSchemeButton };
//# sourceMappingURL=ColorSchemeButton.mjs.map

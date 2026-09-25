"use strict";
var SampleUnit = /* @__PURE__ */ ((SampleUnit2) => {
  SampleUnit2["Bytes"] = "bytes";
  SampleUnit2["Short"] = "short";
  SampleUnit2["Nanoseconds"] = "ns";
  return SampleUnit2;
})(SampleUnit || {});
var SelectedView = /* @__PURE__ */ ((SelectedView2) => {
  SelectedView2["TopTable"] = "topTable";
  SelectedView2["FlameGraph"] = "flameGraph";
  SelectedView2["Both"] = "both";
  return SelectedView2;
})(SelectedView || {});
var ViewMode = /* @__PURE__ */ ((ViewMode2) => {
  ViewMode2["Single"] = "single";
  ViewMode2["Split"] = "split";
  return ViewMode2;
})(ViewMode || {});
var PaneView = /* @__PURE__ */ ((PaneView2) => {
  PaneView2["TopTable"] = "topTable";
  PaneView2["FlameGraph"] = "flameGraph";
  PaneView2["CallTree"] = "callTree";
  return PaneView2;
})(PaneView || {});
var ColorScheme = /* @__PURE__ */ ((ColorScheme2) => {
  ColorScheme2["ValueBased"] = "valueBased";
  ColorScheme2["PackageBased"] = "packageBased";
  return ColorScheme2;
})(ColorScheme || {});
var ColorSchemeDiff = /* @__PURE__ */ ((ColorSchemeDiff2) => {
  ColorSchemeDiff2["Default"] = "default";
  ColorSchemeDiff2["DiffColorBlind"] = "diffColorBlind";
  return ColorSchemeDiff2;
})(ColorSchemeDiff || {});

export { ColorScheme, ColorSchemeDiff, PaneView, SampleUnit, SelectedView, ViewMode };
//# sourceMappingURL=types.mjs.map

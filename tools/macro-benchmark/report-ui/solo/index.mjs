import { jsx, jsxs, Fragment } from 'react/jsx-runtime';
import uFuzzy from '@leeoniya/ufuzzy';
import { useState, useEffect, useMemo, useRef, useCallback, memo, useId } from 'react';
import color from 'tinycolor2';
import { createPortal } from 'react-dom';

const KIB = 1024;
const MIB = KIB * 1024;
const GIB = MIB * 1024;
const TIB = GIB * 1024;
const NS_PER_US = 1e3;
const NS_PER_MS = 1e6;
const NS_PER_S = 1e9;
const NS_PER_MIN = 60 * NS_PER_S;
const NS_PER_HR = 60 * NS_PER_MIN;
const NS_PER_DAY = 24 * NS_PER_HR;
function escapeRegex(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
function formatShort(n) {
  const abs = Math.abs(n);
  if (abs >= 1e12) return scaled(n, 1e12, " Tri");
  if (abs >= 1e9) return scaled(n, 1e9, " Bil");
  if (abs >= 1e6) return scaled(n, 1e6, " Mil");
  if (abs >= 1e3) return scaled(n, 1e3, " K");
  return { text: trimZeros(n.toFixed(2)), suffix: "", numeric: n };
}
function formatDuration(ns) {
  const abs = Math.abs(ns);
  if (abs >= NS_PER_DAY) return scaled(ns, NS_PER_DAY, " day");
  if (abs >= NS_PER_HR) return scaled(ns, NS_PER_HR, " hour");
  if (abs >= NS_PER_MIN) return scaled(ns, NS_PER_MIN, " min");
  if (abs >= NS_PER_S) return scaled(ns, NS_PER_S, " s");
  if (abs >= NS_PER_MS) return scaled(ns, NS_PER_MS, " ms");
  if (abs >= NS_PER_US) return scaled(ns, NS_PER_US, " \xB5s");
  return { text: String(Math.round(ns)), suffix: " ns", numeric: ns };
}
function formatBytes(b) {
  const abs = Math.abs(b);
  if (abs >= TIB) return scaled(b, TIB, " TiB");
  if (abs >= GIB) return scaled(b, GIB, " GiB");
  if (abs >= MIB) return scaled(b, MIB, " MiB");
  if (abs >= KIB) return scaled(b, KIB, " KiB");
  return { text: String(Math.round(b)), suffix: " B", numeric: b };
}
function formatByUnit(value, unit) {
  switch (unit) {
    case "ns":
      return formatDuration(value);
    case "bytes":
      return formatBytes(value);
    case "short":
    default:
      return formatShort(value);
  }
}
function scaled(value, divisor, suffix) {
  return {
    text: trimZeros((value / divisor).toFixed(2)),
    suffix,
    numeric: value
  };
}
function trimZeros(s) {
  if (!s.includes(".")) return s;
  return s.replace(/\.?0+$/, "");
}

const SampleUnit = {
  Bytes: "bytes",
  Nanoseconds: "ns"
};
const ColorScheme = {
  ValueBased: "valueBased",
  PackageBased: "packageBased"
};
const ColorSchemeDiff = {
  Default: "default",
  DiffColorBlind: "diffColorBlind"
};

function groupBy(items, keyFn) {
  const groups = {};
  for (const item of items) {
    const key = keyFn(item);
    (groups[key] = groups[key] || []).push(item);
  }
  return groups;
}
function mergeParentSubtrees(roots, data) {
  const newRoots = getParentSubtrees(roots);
  return mergeSubtrees(newRoots, data, "parents");
}
function getParentSubtrees(roots) {
  return roots.map((r) => {
    if (!r.parents?.length) {
      return r;
    }
    const newRoot = {
      ...r,
      children: []
    };
    const stack = [
      { child: newRoot, parent: r.parents[0] }
    ];
    while (stack.length) {
      const args = stack.shift();
      const newNode = {
        ...args.parent,
        children: args.child ? [args.child] : [],
        parents: []
      };
      if (args.child) {
        newNode.value = args.child.value;
        newNode.valueRight = args.child.valueRight;
        args.child.parents = [newNode];
      }
      if (args.parent.parents?.length) {
        stack.push({ child: newNode, parent: args.parent.parents[0] });
      }
    }
    return newRoot;
  });
}
function mergeSubtrees(roots, data, direction = "children") {
  const oppositeDirection = direction === "parents" ? "children" : "parents";
  const levels = [];
  const stack = [{ previous: void 0, items: roots, level: 0 }];
  while (stack.length) {
    const args = stack.shift();
    const indexes = args.items.flatMap((i) => i.itemIndexes);
    const newItem = {
      // We use the items value instead of value from the data frame, cause we could have changed it in the process
      value: args.items.reduce((acc, i) => acc + i.value, 0),
      valueRight: args.items.some((i) => i.valueRight !== void 0) ? args.items.reduce((acc, i) => acc + (i.valueRight ?? 0), 0) : void 0,
      itemIndexes: indexes,
      // these will change later
      children: [],
      parents: [],
      start: 0,
      level: args.level
    };
    levels[args.level] = levels[args.level] || [];
    levels[args.level].push(newItem);
    if (args.previous) {
      newItem[oppositeDirection] = [args.previous];
      const prevSiblingsVal = args.previous[direction]?.reduce((acc, node) => {
        return acc + node.value;
      }, 0) || 0;
      newItem.start = args.previous.start + prevSiblingsVal;
      args.previous[direction].push(newItem);
    }
    const nextItems = args.items.flatMap((i) => i[direction] || []);
    const nextGroups = groupBy(
      nextItems,
      (c) => data.getLabel(c.itemIndexes[0])
    );
    for (const g of Object.values(nextGroups)) {
      stack.push({ previous: newItem, items: g, level: args.level + 1 });
    }
  }
  if (direction === "parents") {
    levels.reverse();
    levels.forEach((level, index) => {
      level.forEach((item) => {
        item.level = index;
      });
    });
  }
  return levels;
}

const FieldType = {
  string: "string",
  number: "number",
  enum: "enum"
};
function nestedSetToLevels(container, options) {
  const levels = [];
  let offset = 0;
  let parent;
  const uniqueLabels = /* @__PURE__ */ Object.create(null);
  for (let i = 0; i < container.data.length; i++) {
    const currentLevel = container.getLevel(i);
    const prevLevel = i > 0 ? container.getLevel(i - 1) : void 0;
    levels[currentLevel] = levels[currentLevel] || [];
    if (prevLevel && prevLevel >= currentLevel) {
      const lastSibling = levels[currentLevel][levels[currentLevel].length - 1];
      offset = lastSibling.start + lastSibling.value;
      parent = lastSibling.parents[0];
    }
    const newItem = {
      itemIndexes: [i],
      value: container.getValue(i) + container.getValueRight(i),
      valueRight: container.isDiffFlamegraph() ? container.getValueRight(i) : void 0,
      start: offset,
      parents: parent && [parent],
      children: [],
      level: currentLevel
    };
    if (uniqueLabels[container.getLabel(i)]) {
      uniqueLabels[container.getLabel(i)].push(newItem);
    } else {
      uniqueLabels[container.getLabel(i)] = [newItem];
    }
    if (parent) {
      parent.children.push(newItem);
    }
    parent = newItem;
    levels[currentLevel].push(newItem);
  }
  const collapsedMapContainer = new CollapsedMapBuilder(
    options?.collapsingThreshold
  );
  if (options?.collapsing) {
    collapsedMapContainer.addTree(levels[0][0]);
  }
  return [levels, uniqueLabels, collapsedMapContainer.getCollapsedMap()];
}
class CollapsedMap {
  constructor(map) {
    // The levelItem used as a key is the item that will always be rendered in the flame graph. The config.items are all
    // the items that are in the group and if the config.collapsed is true they will be hidden.
    this.map = /* @__PURE__ */ new Map();
    this.map = map || /* @__PURE__ */ new Map();
  }
  get(item) {
    return this.map.get(item);
  }
  keys() {
    return this.map.keys();
  }
  values() {
    return this.map.values();
  }
  size() {
    return this.map.size;
  }
  setCollapsedStatus(item, collapsed) {
    const newMap = new Map(this.map);
    const collapsedConfig = this.map.get(item);
    const newConfig = { ...collapsedConfig, collapsed };
    for (const item2 of collapsedConfig.items) {
      newMap.set(item2, newConfig);
    }
    return new CollapsedMap(newMap);
  }
  /** Reveal a focused node by expanding its group and every collapsed ancestor group. */
  expandForFocus(item) {
    let result = this;
    const pending = [item];
    const seen = /* @__PURE__ */ new Set();
    while (pending.length) {
      const current = pending.pop();
      if (seen.has(current)) continue;
      seen.add(current);
      if (result.get(current)?.collapsed)
        result = result.setCollapsedStatus(current, false);
      pending.push(...current.parents ?? []);
    }
    return result;
  }
  /** Keep search hits visible without changing the user's saved collapse state. */
  expandMatchingLabels(data, labels) {
    if (!labels?.size) return this;
    let result = this;
    for (const label of labels) {
      for (const item of data.getNodesWithLabel(label) ?? []) {
        result = result.expandForFocus(item);
      }
    }
    return result;
  }
  setAllCollapsedStatus(collapsed) {
    const newMap = new Map(this.map);
    for (const item of this.map.keys()) {
      const collapsedConfig = this.map.get(item);
      const newConfig = { ...collapsedConfig, collapsed };
      newMap.set(item, newConfig);
    }
    return new CollapsedMap(newMap);
  }
}
class CollapsedMapBuilder {
  constructor(threshold) {
    this.map = /* @__PURE__ */ new Map();
    this.threshold = 0.99;
    if (threshold !== void 0) {
      this.threshold = threshold;
    }
  }
  addTree(root) {
    const stack = [root];
    while (stack.length) {
      const current = stack.shift();
      if (current.parents?.length) {
        this.addItem(current, current.parents[0]);
      }
      if (current.children.length) {
        stack.unshift(...current.children);
      }
    }
  }
  // The heuristics here is pretty simple right now. Just check if it's single child and if we are within threshold.
  // We assume items with small self just aren't too important while we cannot really collapse items with siblings
  // as it's not clear what to do with said sibling.
  addItem(item, parent) {
    if (parent && item.value > parent.value * this.threshold && parent.children.length === 1) {
      if (this.map.has(parent)) {
        const config = this.map.get(parent);
        this.map.set(item, config);
        config.items.push(item);
      } else {
        const config = { items: [parent, item], collapsed: true };
        this.map.set(parent, config);
        this.map.set(item, config);
      }
    }
  }
  getCollapsedMap() {
    return new CollapsedMap(this.map);
  }
}
function getMessageCheckFieldsResult(wrongFields) {
  if (wrongFields.missingFields.length) {
    return `Data is missing fields: ${wrongFields.missingFields.join(", ")}`;
  }
  if (wrongFields.wrongTypeFields.length) {
    return `Data has fields of wrong type: ${wrongFields.wrongTypeFields.map(
      (f) => `${f.name} has type ${f.type} but should be ${f.expectedTypes.join(" or ")}`
    ).join(", ")}`;
  }
  return "";
}
function checkFields(data) {
  const fields = [
    ["label", [FieldType.string, FieldType.enum]],
    ["level", [FieldType.number]],
    ["value", [FieldType.number]],
    ["self", [FieldType.number]]
  ];
  const missingFields = [];
  const wrongTypeFields = [];
  for (const field of fields) {
    const [name, types] = field;
    const frameField = data?.fields.find((f) => f.name === name);
    if (!frameField) {
      missingFields.push(name);
      continue;
    }
    if (!types.includes(frameField.type)) {
      wrongTypeFields.push({
        name,
        expectedTypes: types,
        type: frameField.type
      });
    }
  }
  if (missingFields.length > 0 || wrongTypeFields.length > 0) {
    return {
      wrongTypeFields,
      missingFields
    };
  }
  return void 0;
}
class FlameGraphDataContainer {
  constructor(data, options) {
    this.data = data;
    this.options = options;
    const wrongFields = checkFields(data);
    if (wrongFields) {
      throw new Error(getMessageCheckFieldsResult(wrongFields));
    }
    this.labelField = data.fields.find((f) => f.name === "label");
    this.levelField = data.fields.find((f) => f.name === "level");
    this.valueField = data.fields.find((f) => f.name === "value");
    this.selfField = data.fields.find((f) => f.name === "self");
    this.valueRightField = data.fields.find((f) => f.name === "valueRight");
    this.selfRightField = data.fields.find((f) => f.name === "selfRight");
    if (Boolean(this.valueRightField) !== Boolean(this.selfRightField)) {
      throw new Error(
        "Diff profiles require both valueRight and selfRight fields."
      );
    }
    for (const field of [this.valueRightField, this.selfRightField]) {
      if (field && field.type !== FieldType.number)
        throw new Error(`${field.name} must be numeric.`);
    }
    const enumConfig = this.labelField?.config?.type?.enum;
    if (enumConfig) {
      const lookup = enumConfig.text || [];
      this.labelDisplayProcessor = (value) => ({
        text: typeof value === "number" ? lookup[value] ?? String(value) : String(value),
        suffix: "",
        numeric: 0
      });
      this.uniqueLabels = lookup;
    } else {
      this.labelDisplayProcessor = (value) => ({
        text: String(value),
        suffix: "",
        numeric: 0
      });
      this.uniqueLabels = [
        ...new Set(this.labelField.values)
      ];
    }
    const unit = this.valueField.config.unit;
    this.valueDisplayProcessor = (value) => formatByUnit(Number(value), unit);
  }
  getLabel(index) {
    return this.labelDisplayProcessor(this.labelField.values[index]).text;
  }
  getLevel(index) {
    return Number(this.levelField.values[index]);
  }
  getValue(index) {
    return fieldAccessor(this.valueField, index);
  }
  getSelf(index) {
    return fieldAccessor(this.selfField, index);
  }
  isDiffFlamegraph() {
    return Boolean(this.valueRightField && this.selfRightField);
  }
  getValueRight(index) {
    return fieldAccessor(this.valueRightField, index);
  }
  getSelfRight(index) {
    return fieldAccessor(this.selfRightField, index);
  }
  getSelfDisplay(index) {
    return this.valueDisplayProcessor(this.getSelf(index));
  }
  getUniqueLabels() {
    return this.uniqueLabels;
  }
  getUnitTitle() {
    switch (this.valueField.config.unit) {
      case SampleUnit.Bytes:
        return "RAM";
      case SampleUnit.Nanoseconds:
        return "Time";
    }
    return "Count";
  }
  getLevels() {
    this.initLevels();
    return this.levels;
  }
  getSandwichLevels(label) {
    const nodes = this.getNodesWithLabel(label);
    if (!nodes?.length) {
      return [[], []];
    }
    const callers = mergeParentSubtrees(nodes, this);
    const callees = mergeSubtrees(nodes, this);
    return [callers, callees];
  }
  getNodesWithLabel(label) {
    this.initLevels();
    return this.uniqueLabelsMap[label];
  }
  getCollapsedMap() {
    this.initLevels();
    return this.collapsedMap;
  }
  initLevels() {
    if (!this.levels) {
      const [levels, uniqueLabelsMap, collapsedMap] = nestedSetToLevels(
        this,
        this.options
      );
      this.levels = levels;
      this.uniqueLabelsMap = uniqueLabelsMap;
      this.collapsedMap = collapsedMap;
    }
  }
}
function fieldAccessor(field, index) {
  if (!field) {
    return 0;
  }
  const indexArray = typeof index === "number" ? [index] : index;
  return indexArray.reduce(
    (acc, i) => acc + Number(field.values[i]),
    0
  );
}

const paths = {
  "angle-double-down": /* @__PURE__ */ jsx(Fragment, { children: /* @__PURE__ */ jsx("path", { d: "m4 5 4 4 4-4M4 10l4 4 4-4" }) }),
  "angle-double-up": /* @__PURE__ */ jsx(Fragment, { children: /* @__PURE__ */ jsx("path", { d: "m4 11 4-4 4 4M4 6l4-4 4 4" }) }),
  "angle-down": /* @__PURE__ */ jsx("path", { d: "m3 6 5 5 5-5" }),
  "angle-up": /* @__PURE__ */ jsx("path", { d: "m3 10 5-5 5 5" }),
  "angle-right": /* @__PURE__ */ jsx("path", { d: "m6 3 5 5-5 5" }),
  "align-left": /* @__PURE__ */ jsx("path", { d: "M2 3h12M2 6h8M2 9h12M2 12h8" }),
  "align-right": /* @__PURE__ */ jsx("path", { d: "M2 3h12M6 6h8M2 9h12M6 12h8" }),
  "history-alt": /* @__PURE__ */ jsxs(Fragment, { children: [
    /* @__PURE__ */ jsx("path", { d: "M3 5a6 6 0 1 1-1 4M3 1v4h4" }),
    /* @__PURE__ */ jsx("path", { d: "M8 5v4l2 1" })
  ] }),
  times: /* @__PURE__ */ jsx("path", { d: "M3 3 13 13M13 3 3 13" }),
  search: /* @__PURE__ */ jsxs(Fragment, { children: [
    /* @__PURE__ */ jsx("circle", { cx: "7", cy: "7", r: "4" }),
    /* @__PURE__ */ jsx("path", { d: "m10 10 4 4" })
  ] }),
  sandwich: /* @__PURE__ */ jsx(Fragment, { children: /* @__PURE__ */ jsx("path", { d: "M2 4h12M4 8h8M2 12h12" }) }),
  copy: /* @__PURE__ */ jsxs(Fragment, { children: [
    /* @__PURE__ */ jsx("rect", { x: "5", y: "5", width: "9", height: "9", rx: "1" }),
    /* @__PURE__ */ jsx("path", { d: "M2 11V2h9" })
  ] }),
  eye: /* @__PURE__ */ jsxs(Fragment, { children: [
    /* @__PURE__ */ jsx("path", { d: "M1 8s3-5 7-5 7 5 7 5-3 5-7 5-7-5-7-5Z" }),
    /* @__PURE__ */ jsx("circle", { cx: "8", cy: "8", r: "2" })
  ] }),
  "exclamation-circle": /* @__PURE__ */ jsxs(Fragment, { children: [
    /* @__PURE__ */ jsx("circle", { cx: "8", cy: "8", r: "6" }),
    /* @__PURE__ */ jsx("path", { d: "M8 4v5M8 12h.01" })
  ] })
};
function Icon({
  name,
  size = 14,
  className
}) {
  return /* @__PURE__ */ jsx(
    "svg",
    {
      className,
      width: size,
      height: size,
      viewBox: "0 0 16 16",
      fill: "none",
      stroke: "currentColor",
      strokeWidth: "1.7",
      strokeLinecap: "round",
      strokeLinejoin: "round",
      role: "presentation",
      "aria-hidden": "true",
      children: paths[name] ?? /* @__PURE__ */ jsx("circle", { cx: "8", cy: "8", r: "3" })
    }
  );
}

function murmurhash3_32_gc(key, seed = 0) {
  let remainder;
  let bytes;
  let h1;
  let h1b;
  let c1;
  let c2;
  let k1;
  let i;
  remainder = key.length & 3;
  bytes = key.length - remainder;
  h1 = seed;
  c1 = 3432918353;
  c2 = 461845907;
  i = 0;
  while (i < bytes) {
    k1 = key.charCodeAt(i) & 255 | (key.charCodeAt(++i) & 255) << 8 | (key.charCodeAt(++i) & 255) << 16 | (key.charCodeAt(++i) & 255) << 24;
    ++i;
    k1 = (k1 & 65535) * c1 + (((k1 >>> 16) * c1 & 65535) << 16) & 4294967295;
    k1 = k1 << 15 | k1 >>> 17;
    k1 = (k1 & 65535) * c2 + (((k1 >>> 16) * c2 & 65535) << 16) & 4294967295;
    h1 ^= k1;
    h1 = h1 << 13 | h1 >>> 19;
    h1b = (h1 & 65535) * 5 + (((h1 >>> 16) * 5 & 65535) << 16) & 4294967295;
    h1 = (h1b & 65535) + 27492 + (((h1b >>> 16) + 58964 & 65535) << 16);
  }
  k1 = 0;
  switch (remainder) {
    case 3:
      k1 ^= (key.charCodeAt(i + 2) & 255) << 16;
    // fall through
    case 2:
      k1 ^= (key.charCodeAt(i + 1) & 255) << 8;
    // fall through
    case 1:
      k1 ^= key.charCodeAt(i) & 255;
    // fall through
    default:
      k1 = (k1 & 65535) * c1 + (((k1 >>> 16) * c1 & 65535) << 16) & 4294967295;
      k1 = k1 << 15 | k1 >>> 17;
      k1 = (k1 & 65535) * c2 + (((k1 >>> 16) * c2 & 65535) << 16) & 4294967295;
      h1 ^= k1;
  }
  h1 ^= key.length;
  h1 ^= h1 >>> 16;
  h1 = (h1 & 65535) * 2246822507 + (((h1 >>> 16) * 2246822507 & 65535) << 16) & 4294967295;
  h1 ^= h1 >>> 13;
  h1 = (h1 & 65535) * 3266489909 + (((h1 >>> 16) * 3266489909 & 65535) << 16) & 4294967295;
  h1 ^= h1 >>> 16;
  return h1 >>> 0;
}

const packageColors = [
  color({ h: 24, s: 69, l: 60 }),
  color({ h: 34, s: 65, l: 65 }),
  color({ h: 194, s: 52, l: 61 }),
  color({ h: 163, s: 45, l: 55 }),
  color({ h: 211, s: 48, l: 60 }),
  color({ h: 246, s: 40, l: 65 }),
  color({ h: 305, s: 63, l: 79 }),
  color({ h: 47, s: 100, l: 73 }),
  color({ r: 183, g: 219, b: 171 }),
  color({ r: 244, g: 213, b: 152 }),
  color({ r: 78, g: 146, b: 249 }),
  color({ r: 249, g: 186, b: 143 }),
  color({ r: 242, g: 145, b: 145 }),
  color({ r: 130, g: 181, b: 216 }),
  color({ r: 229, g: 168, b: 226 }),
  color({ r: 174, g: 162, b: 224 }),
  color({ r: 154, g: 196, b: 138 }),
  color({ r: 242, g: 201, b: 109 }),
  color({ r: 101, g: 197, b: 219 }),
  color({ r: 249, g: 147, b: 78 }),
  color({ r: 234, g: 100, b: 96 }),
  color({ r: 81, g: 149, b: 206 }),
  color({ r: 214, g: 131, b: 206 }),
  color({ r: 128, g: 110, b: 183 })
];
const byValueMinColor = getBarColorByValue(1, 100, 0, 1);
const byValueMaxColor = getBarColorByValue(100, 100, 0, 1);
const byValueGradient = `linear-gradient(90deg, ${byValueMinColor} 0%, ${byValueMaxColor} 100%)`;
const byPackageGradient = `linear-gradient(90deg, ${packageColors[0]} 0%, ${packageColors[2]} 30%, ${packageColors[6]} 50%, ${packageColors[7]} 70%, ${packageColors[8]} 100%)`;
function getBarColorByValue(value, totalTicks, rangeMin, rangeMax) {
  const intensity = Math.min(1, value / totalTicks / (rangeMax - rangeMin));
  const h = 50 - 50 * intensity;
  const l = 65 + 7 * intensity;
  return color({ h, s: 100, l });
}
function getBarColorByPackage(label, isLight) {
  const packageName = getPackageName(label);
  const hash = murmurhash3_32_gc(packageName || "", 0);
  const colorIndex = hash % packageColors.length;
  let packageColor = packageColors[colorIndex].clone();
  if (isLight) {
    packageColor = packageColor.brighten(15);
  }
  return packageColor;
}
const matchers = [
  [
    "phpspy",
    /^(?<packageName>([^/]*\/)*)(?<filename>.*\.php+)(?<line_info>.*)$/
  ],
  ["pyspy", /^(?<packageName>([^/]*\/)*)(?<filename>.*\.py+)(?<line_info>.*)$/],
  ["rbspy", /^(?<packageName>([^/]*\/)*)(?<filename>.*\.rb+)(?<line_info>.*)$/],
  [
    "nodespy",
    /^(\.\/node_modules\/)?(?<packageName>[^/]*)(?<filename>.*\.?(jsx?|tsx?)?):(?<functionName>.*):(?<line_info>.*)$/
  ],
  ["gospy", /^(?<packageName>.*?\/.*?\.|.*?\.|.+)(?<functionName>.*)$/],
  // also 'scrape'
  ["javaspy", /^(?<packageName>.+\/)(?<filename>.+\.)(?<functionName>.+)$/],
  ["dotnetspy", /^(?<packageName>.+)\.(.+)\.(.+)\(.*\)$/],
  ["tracing", /^(?<packageName>.+?):.*$/],
  ["pyroscope-rs", /^(?<packageName>[^::]+)/],
  ["ebpfspy", /^(?<packageName>.+)$/],
  ["unknown", /^(?<packageName>.+)$/]
];
function getPackageName(name) {
  for (const [_, matcher] of matchers) {
    const match = name.match(matcher);
    if (match) {
      return match.groups?.packageName || "";
    }
  }
  return void 0;
}
const diffDefaultColors = [
  "rgb(0, 170, 0)",
  "rgb(148, 142, 142)",
  "rgb(200, 0, 0)"
];
const diffDefaultGradient = `linear-gradient(90deg, ${diffDefaultColors[0]} 0%, ${diffDefaultColors[1]} 50%, ${diffDefaultColors[2]} 100%)`;
const diffColorBlindColors = [
  "rgb(26, 133, 255)",
  "rgb(148, 142, 142)",
  "rgb(220, 50, 32)"
];
const diffColorBlindGradient = `linear-gradient(90deg, ${diffColorBlindColors[0]} 0%, ${diffColorBlindColors[1]} 50%, ${diffColorBlindColors[2]} 100%)`;
function getBarColorByDiff(ticks, ticksRight, totalTicks, totalTicksRight, colorScheme, diffMode = "proportionalDiff") {
  const range = colorScheme === "default" ? diffDefaultColors : diffColorBlindColors;
  const ticksLeft = ticks - ticksRight;
  const totalTicksLeft = totalTicks - totalTicksRight;
  const diff = diffMode === "absoluteDiff" ? (ticksRight - ticksLeft) / (Math.max(totalTicksLeft, totalTicksRight) || 1) * 100 : totalTicksLeft && totalTicksRight ? (() => {
    const leftShare = ticksLeft / totalTicksLeft;
    const rightShare = ticksRight / totalTicksRight;
    return leftShare ? (rightShare - leftShare) / leftShare * 100 : rightShare ? 100 : 0;
  })() : 0;
  const [from, to, fraction] = diff < 0 ? [range[0], range[1], (diff + 100) / 100] : [range[1], range[2], diff / 100];
  const a = color(from).toRgb();
  const b = color(to).toRgb();
  const channel = (left, right) => Math.max(0, Math.min(255, Math.round(left + (right - left) * fraction)));
  return color({
    r: channel(a.r, b.r),
    g: channel(a.g, b.g),
    b: channel(a.b, b.b)
  });
}

function getSymbolColor(label, value, total, right, totalRight, isLight, scheme, diffMode = "proportionalDiff") {
  if (scheme === "default" || scheme === "diffColorBlind") {
    return getBarColorByDiff(
      value,
      right,
      total,
      totalRight,
      scheme === "diffColorBlind" ? "diffColorBlind" : "default",
      diffMode
    ).toHexString();
  }
  if (scheme === "valueBased")
    return getBarColorByValue(value, total, 0, 1).toHexString();
  return getBarColorByPackage(label, isLight).toHexString();
}
function TableMetricCell({
  value,
  total,
  display,
  color,
  label
}) {
  const percent = total > 0 ? Math.min(100, Math.max(0, value / total * 100)) : 0;
  return /* @__PURE__ */ jsxs("td", { className: "fg-tt-numeric-cell fg-tt-metric-cell", children: [
    /* @__PURE__ */ jsx("span", { children: display }),
    /* @__PURE__ */ jsx(
      "div",
      {
        className: "fg-tt-metric-track",
        role: "img",
        "aria-label": `${label}: ${percent.toFixed(2)}% of profile`,
        children: /* @__PURE__ */ jsx(
          "div",
          {
            className: "fg-tt-metric-bar",
            style: { width: `${percent}%`, backgroundColor: color }
          }
        )
      }
    )
  ] });
}

function useIsLight() {
  const [isLight, setIsLight] = useState(getIsLight);
  useEffect(() => {
    const observer = new MutationObserver(() => setIsLight(getIsLight()));
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"]
    });
    return () => observer.disconnect();
  }, []);
  return isLight;
}
function getIsLight() {
  if (typeof document === "undefined") return false;
  return document.documentElement.getAttribute("data-theme") === "light";
}
function cssVar(name) {
  if (typeof getComputedStyle === "undefined") return "";
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

function StacktraceTable({
  data,
  diffMode,
  colorScheme,
  search,
  onSymbolClick
}) {
  const isLight = useIsLight();
  const [expanded, setExpanded] = useState(() => /* @__PURE__ */ new Set([0]));
  const [sort, setSort] = useState(
    { column: "total", descending: true }
  );
  const roots = data.getLevels()[0] ?? [];
  const total = roots.reduce((sum, root) => sum + root.value, 0);
  const totalRight = roots.reduce(
    (sum, root) => sum + (root.valueRight ?? 0),
    0
  );
  const totalLeft = total - totalRight;
  const isDiff = data.isDiffFlamegraph();
  const matcher = useMemo(() => {
    if (!search) return void 0;
    try {
      return new RegExp(search, "i");
    } catch {
      return void 0;
    }
  }, [search]);
  const rows = [];
  const visit = (nodes, continuations = []) => {
    const sorted = [...nodes].sort((a, b) => {
      const value = (item) => sort.column === "self" ? data.getSelf(item.itemIndexes) : item.value;
      return (value(a) - value(b)) * (sort.descending ? -1 : 1);
    });
    sorted.forEach((node, index) => {
      const hasNext = index < sorted.length - 1;
      rows.push({ node, continuations, hasNext });
      if (expanded.has(node.itemIndexes[0]))
        visit(node.children, [...continuations, hasNext]);
    });
  };
  visit(roots);
  const format = (value) => {
    const display = data.valueDisplayProcessor(value);
    return `${display.text}${display.suffix ?? ""}`;
  };
  const sortHeader = (column) => /* @__PURE__ */ jsxs(
    "button",
    {
      type: "button",
      className: "fg-tt-sort-btn",
      "aria-label": `Sort by ${column}`,
      onClick: () => setSort((current) => ({
        column,
        descending: current.column === column ? !current.descending : true
      })),
      children: [
        column === "self" ? "Self" : "Total",
        sort.column === column && /* @__PURE__ */ jsx(Icon, { name: sort.descending ? "angle-down" : "angle-up", size: 12 })
      ]
    }
  );
  return /* @__PURE__ */ jsx(
    "div",
    {
      className: `fg-tt-container fg-stacktrace${isDiff ? " fg-stacktrace-diff" : ""}`,
      "data-testid": "callTree",
      children: /* @__PURE__ */ jsx("div", { className: "fg-tt-scroll", children: /* @__PURE__ */ jsxs("table", { className: "fg-tt-table", children: [
        /* @__PURE__ */ jsx("thead", { className: "fg-tt-thead", children: /* @__PURE__ */ jsxs("tr", { className: "fg-tt-header-row", children: [
          /* @__PURE__ */ jsx(
            "th",
            {
              className: "fg-tt-action-header",
              "aria-label": "Expand or collapse"
            }
          ),
          /* @__PURE__ */ jsx("th", { className: "fg-tt-symbol-header", children: "Function" }),
          isDiff ? /* @__PURE__ */ jsxs(Fragment, { children: [
            /* @__PURE__ */ jsx("th", { className: "fg-tt-numeric-header", children: "Baseline" }),
            /* @__PURE__ */ jsx("th", { className: "fg-tt-numeric-header", children: "Comparison" }),
            /* @__PURE__ */ jsx("th", { className: "fg-tt-numeric-header", children: "Diff" })
          ] }) : /* @__PURE__ */ jsxs(Fragment, { children: [
            /* @__PURE__ */ jsx("th", { className: "fg-tt-numeric-header", children: sortHeader("self") }),
            /* @__PURE__ */ jsx("th", { className: "fg-tt-numeric-header", children: sortHeader("total") })
          ] })
        ] }) }),
        /* @__PURE__ */ jsx("tbody", { children: rows.map(({ node, continuations, hasNext }) => {
          const id = node.itemIndexes[0];
          const label = data.getLabel(id);
          const self = data.getSelf(node.itemIndexes);
          const symbolColor = getSymbolColor(
            label,
            node.value,
            total,
            node.valueRight ?? 0,
            totalRight,
            isLight,
            colorScheme ?? "packageBased",
            diffMode
          );
          const metricCell = (value, profileTotal, display, title) => /* @__PURE__ */ jsx(
            TableMetricCell,
            {
              label: title,
              value,
              total: profileTotal,
              display,
              color: symbolColor
            }
          );
          const isExpanded = expanded.has(id);
          const numericCell = (value, color) => /* @__PURE__ */ jsx(
            "td",
            {
              className: "fg-tt-numeric-cell",
              style: color ? { color } : void 0,
              children: value
            }
          );
          return /* @__PURE__ */ jsxs(
            "tr",
            {
              className: `fg-tt-row${matcher?.test(label) ? " fg-stacktrace-match" : ""}`,
              style: { height: 25 },
              children: [
                /* @__PURE__ */ jsx("td", { className: "fg-tt-action-cell", children: node.children.length > 0 && /* @__PURE__ */ jsx(
                  "button",
                  {
                    type: "button",
                    className: "fg-tt-action-btn",
                    "aria-label": `${isExpanded ? "Collapse" : "Expand"} ${label}`,
                    "aria-expanded": isExpanded,
                    onClick: () => setExpanded((prev) => {
                      const next = new Set(prev);
                      if (next.has(id)) next.delete(id);
                      else next.add(id);
                      return next;
                    }),
                    children: /* @__PURE__ */ jsx(
                      Icon,
                      {
                        name: isExpanded ? "angle-down" : "angle-right",
                        size: 14
                      }
                    )
                  }
                ) }),
                /* @__PURE__ */ jsx("td", { className: "fg-tt-symbol-cell", title: label, children: /* @__PURE__ */ jsxs("div", { className: "fg-stacktrace-symbol", children: [
                  node.level > 0 && /* @__PURE__ */ jsxs(Fragment, { children: [
                    continuations.slice(1).map((continues, depth) => /* @__PURE__ */ jsx(
                      "span",
                      {
                        className: `fg-stacktrace-guide${continues ? " fg-stacktrace-guide-continuation" : ""}`,
                        "aria-hidden": "true"
                      },
                      depth
                    )),
                    /* @__PURE__ */ jsx(
                      "span",
                      {
                        className: `fg-stacktrace-guide fg-stacktrace-branch${hasNext ? " fg-stacktrace-guide-continuation" : ""}`,
                        "aria-hidden": "true"
                      }
                    )
                  ] }),
                  /* @__PURE__ */ jsx(
                    "button",
                    {
                      type: "button",
                      className: "fg-tt-symbol-link fg-stacktrace-link",
                      onClick: () => onSymbolClick?.(label),
                      children: label
                    }
                  )
                ] }) }),
                isDiff ? (() => {
                  const left = node.value - (node.valueRight ?? 0), right = node.valueRight ?? 0;
                  if (diffMode === "absoluteDiff") {
                    const delta = right - left;
                    return /* @__PURE__ */ jsxs(Fragment, { children: [
                      metricCell(
                        left,
                        totalLeft,
                        format(left),
                        "Baseline"
                      ),
                      metricCell(
                        right,
                        totalRight,
                        format(right),
                        "Comparison"
                      ),
                      numericCell(
                        `${delta >= 0 ? "+" : "\u2212"}${format(Math.abs(delta))}`,
                        delta > 0 ? "var(--red-300)" : "var(--green-300)"
                      )
                    ] });
                  }
                  const baseline = totalLeft ? left / totalLeft * 100 : 0;
                  const comparison = totalRight ? right / totalRight * 100 : 0;
                  const change = baseline ? (comparison - baseline) / baseline * 100 : 0;
                  return /* @__PURE__ */ jsxs(Fragment, { children: [
                    metricCell(
                      left,
                      totalLeft,
                      `${baseline.toFixed(2)}%`,
                      "Baseline"
                    ),
                    metricCell(
                      right,
                      totalRight,
                      `${comparison.toFixed(2)}%`,
                      "Comparison"
                    ),
                    numericCell(
                      baseline ? `${change.toFixed(2)}%` : "\u2014",
                      change > 0 ? "var(--red-300)" : "var(--green-300)"
                    )
                  ] });
                })() : /* @__PURE__ */ jsxs(Fragment, { children: [
                  metricCell(self, total, format(self), "Self"),
                  metricCell(
                    node.value,
                    total,
                    format(node.value),
                    "Total"
                  )
                ] })
              ]
            },
            id
          );
        }) })
      ] }) })
    }
  );
}

const PIXELS_PER_LEVEL = 22 * window.devicePixelRatio;
const MUTE_THRESHOLD = 10 * window.devicePixelRatio;
const HIDE_THRESHOLD = 0.5 * window.devicePixelRatio;
const LABEL_THRESHOLD = 20 * window.devicePixelRatio;
const BAR_BORDER_WIDTH = 0.5 * window.devicePixelRatio;
const BAR_TEXT_PADDING_LEFT = 4 * window.devicePixelRatio;
const GROUP_STRIP_WIDTH = 3 * window.devicePixelRatio;
const GROUP_STRIP_PADDING = 3 * window.devicePixelRatio;
const GROUP_STRIP_MARGIN_LEFT = 4 * window.devicePixelRatio;
const GROUP_TEXT_OFFSET = 2 * window.devicePixelRatio;

function cx(...args) {
  const out = [];
  for (const a of args) {
    if (!a) continue;
    if (typeof a === "string") out.push(a);
    else for (const k in a) if (a[k]) out.push(k);
  }
  return out.join(" ");
}

function useColorScheme(_dataContainer) {
  const isDiff = _dataContainer?.isDiffFlamegraph() ?? false;
  const [scheme, setScheme] = useState(
    isDiff ? ColorSchemeDiff.Default : ColorScheme.PackageBased
  );
  useEffect(() => {
    setScheme(isDiff ? ColorSchemeDiff.Default : ColorScheme.PackageBased);
  }, [isDiff]);
  return [scheme, setScheme];
}
function useDebounce(fn, delay, deps) {
  const cbRef = useRef(fn);
  cbRef.current = fn;
  useEffect(() => {
    const id = window.setTimeout(() => cbRef.current(), delay);
    return () => window.clearTimeout(id);
  }, deps);
}
function usePrevious(value) {
  const ref = useRef(void 0);
  useEffect(() => {
    ref.current = value;
  }, [value]);
  return ref.current;
}
function useMeasure() {
  const ref = useRef(null);
  const [rect, setRect] = useState({ width: 0, height: 0 });
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver((entries) => {
      for (const entry of entries) {
        const r = entry.contentRect;
        setRect({ width: r.width, height: r.height });
      }
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, rect];
}

const FlameGraphContextMenu = ({
  data,
  itemData,
  onMenuItemClick,
  onItemFocus,
  onSandwich,
  collapseConfig,
  onExpandGroup,
  onCollapseGroup,
  onExpandAllGroups,
  onCollapseAllGroups,
  getExtraContextMenuButtons,
  collapsing,
  allGroupsExpanded,
  allGroupsCollapsed,
  selectedView,
  search
}) => {
  const extraButtons = getExtraContextMenuButtons?.(itemData, data.data, {
    selectedView,
    search,
    collapseConfig
  }) || [];
  return /* @__PURE__ */ jsxs(
    ContextMenu,
    {
      x: itemData.posX + 10,
      y: itemData.posY,
      onClose: onMenuItemClick,
      testId: "contextMenu",
      children: [
        /* @__PURE__ */ jsx(
          MenuItem,
          {
            label: "Focus block",
            icon: "eye",
            onClick: () => {
              onItemFocus();
              onMenuItemClick();
            }
          }
        ),
        /* @__PURE__ */ jsx(
          MenuItem,
          {
            label: "Copy function name",
            icon: "copy",
            onClick: () => {
              navigator.clipboard.writeText(itemData.label).then(() => {
                onMenuItemClick();
              });
            }
          }
        ),
        /* @__PURE__ */ jsx(
          MenuItem,
          {
            label: "Sandwich view",
            icon: "sandwich",
            onClick: () => {
              onSandwich();
              onMenuItemClick();
            }
          }
        ),
        extraButtons.map(({ label, icon, onClick }) => /* @__PURE__ */ jsx(MenuItem, { label, icon, onClick }, label)),
        collapsing && /* @__PURE__ */ jsxs(MenuGroup, { label: "Grouping", children: [
          collapseConfig ? collapseConfig.collapsed ? /* @__PURE__ */ jsx(
            MenuItem,
            {
              label: "Expand group",
              icon: "angle-double-down",
              onClick: () => {
                onExpandGroup();
                onMenuItemClick();
              }
            }
          ) : /* @__PURE__ */ jsx(
            MenuItem,
            {
              label: "Collapse group",
              icon: "angle-double-up",
              onClick: () => {
                onCollapseGroup();
                onMenuItemClick();
              }
            }
          ) : null,
          !allGroupsExpanded && /* @__PURE__ */ jsx(
            MenuItem,
            {
              label: "Expand all groups",
              icon: "angle-double-down",
              onClick: () => {
                onExpandAllGroups();
                onMenuItemClick();
              }
            }
          ),
          !allGroupsCollapsed && /* @__PURE__ */ jsx(
            MenuItem,
            {
              label: "Collapse all groups",
              icon: "angle-double-up",
              onClick: () => {
                onCollapseAllGroups();
                onMenuItemClick();
              }
            }
          )
        ] })
      ]
    }
  );
};
function ContextMenu({
  x,
  y,
  onClose,
  testId,
  children
}) {
  const ref = useRef(null);
  useEffect(() => {
    const onDocClick = (e) => {
      if (!ref.current?.contains(e.target)) onClose();
    };
    const onKey = (e) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("mousedown", onDocClick);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDocClick);
      document.removeEventListener("keydown", onKey);
    };
  }, [onClose]);
  const maxLeft = typeof window !== "undefined" ? window.innerWidth - 220 : x;
  const maxTop = typeof window !== "undefined" ? window.innerHeight - 280 : y;
  const left = Math.min(x, maxLeft);
  const top = Math.min(y, Math.max(0, maxTop));
  return createPortal(
    /* @__PURE__ */ jsx(
      "div",
      {
        ref,
        "data-testid": testId,
        role: "menu",
        className: "fg-ctx-menu",
        style: { left, top },
        children
      }
    ),
    document.body
  );
}
function MenuItem({
  label,
  icon,
  onClick
}) {
  return /* @__PURE__ */ jsxs(
    "button",
    {
      type: "button",
      role: "menuitem",
      className: "fg-ctx-item",
      onClick,
      children: [
        icon && /* @__PURE__ */ jsx(Icon, { name: icon, size: 14 }),
        /* @__PURE__ */ jsx("span", { children: label })
      ]
    }
  );
}
function MenuGroup({
  label,
  children
}) {
  return /* @__PURE__ */ jsxs("div", { role: "group", "aria-label": label, className: "fg-ctx-group", children: [
    /* @__PURE__ */ jsx("div", { className: "fg-ctx-group-label", children: label }),
    children
  ] });
}

const FlameGraphTooltip = ({
  data,
  item,
  totalTicks,
  diffMode,
  position,
  collapseConfig
}) => {
  if (!(item && position)) {
    return null;
  }
  const tooltipData = getTooltipData(data, item, totalTicks);
  const diff = data.isDiffFlamegraph() ? getDiffTooltipData(data, item, totalTicks, diffMode) : void 0;
  return createPortal(
    /* @__PURE__ */ jsx(
      "div",
      {
        className: "fg-tooltip",
        style: { left: position.x + 15, top: position.y },
        role: "tooltip",
        "aria-live": "polite",
        children: /* @__PURE__ */ jsxs("div", { className: "fg-tooltip-content", children: [
          /* @__PURE__ */ jsxs("p", { className: "fg-tooltip-name", children: [
            data.getLabel(item.itemIndexes[0]),
            collapseConfig && collapseConfig.collapsed ? /* @__PURE__ */ jsxs("span", { children: [
              /* @__PURE__ */ jsx("br", {}),
              "and ",
              collapseConfig.items.length,
              " similar items"
            ] }) : ""
          ] }),
          diff && /* @__PURE__ */ jsxs("table", { className: "fg-tooltip-diff", children: [
            /* @__PURE__ */ jsx("thead", { children: /* @__PURE__ */ jsxs("tr", { children: [
              /* @__PURE__ */ jsx("th", {}),
              /* @__PURE__ */ jsx("th", { children: "Baseline" }),
              /* @__PURE__ */ jsx("th", { children: "Comparison" }),
              /* @__PURE__ */ jsx("th", { children: "Diff" })
            ] }) }),
            /* @__PURE__ */ jsxs("tbody", { children: [
              /* @__PURE__ */ jsxs("tr", { children: [
                /* @__PURE__ */ jsx("th", { children: diffMode === "absoluteDiff" ? "Samples" : "Total" }),
                /* @__PURE__ */ jsx("td", { children: diff.baseline }),
                /* @__PURE__ */ jsx("td", { children: diff.comparison }),
                /* @__PURE__ */ jsx("td", { children: diff.change })
              ] }),
              /* @__PURE__ */ jsxs("tr", { children: [
                /* @__PURE__ */ jsx("th", { children: "Self" }),
                /* @__PURE__ */ jsx("td", { children: diff.selfBaseline }),
                /* @__PURE__ */ jsx("td", { children: diff.selfComparison }),
                /* @__PURE__ */ jsx("td", {})
              ] })
            ] })
          ] }),
          !diff && /* @__PURE__ */ jsxs("p", { className: "fg-tooltip-last", children: [
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
          ] })
        ] })
      }
    ),
    document.body
  );
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
function getDiffTooltipData(data, item, totalTicks, diffMode = "proportionalDiff") {
  const totalRight = data.getLevels()[0]?.[0]?.valueRight ?? 0;
  const totalLeft = totalTicks - totalRight;
  const right = item.valueRight ?? 0;
  const left = item.value - right;
  const pct = (value, total) => total ? value / total * 100 : 0;
  const baseline = pct(left, totalLeft);
  const comparison = pct(right, totalRight);
  if (diffMode === "absoluteDiff") {
    const formatted = data.valueDisplayProcessor(Math.abs(right - left));
    return {
      baseline: data.valueDisplayProcessor(left).text + data.valueDisplayProcessor(left).suffix,
      comparison: data.valueDisplayProcessor(right).text + data.valueDisplayProcessor(right).suffix,
      change: `${right >= left ? "+" : "\u2212"}${formatted.text}${formatted.suffix ?? ""}`,
      selfBaseline: data.valueDisplayProcessor(data.getSelf(item.itemIndexes)).text,
      selfComparison: data.valueDisplayProcessor(
        data.getSelfRight(item.itemIndexes)
      ).text
    };
  }
  return {
    baseline: `${baseline.toFixed(2)}%`,
    comparison: `${comparison.toFixed(2)}%`,
    change: baseline ? `${((comparison - baseline) / baseline * 100).toFixed(2)}%` : "\u2014",
    selfBaseline: `${pct(data.getSelf(item.itemIndexes), totalLeft).toFixed(2)}%`,
    selfComparison: `${pct(data.getSelfRight(item.itemIndexes), totalRight).toFixed(2)}%`
  };
}

function useFlameRender(options) {
  const {
    canvasRef,
    data,
    root,
    depth,
    direction,
    wrapperWidth,
    rangeMin,
    rangeMax,
    matchedLabels,
    textAlign,
    totalViewTicks,
    totalColorTicks,
    totalTicksRight,
    diffMode,
    colorScheme,
    focusedItemData,
    collapsedMap
  } = options;
  const ctx = useSetupCanvas(canvasRef, wrapperWidth, depth);
  const isLight = useIsLight();
  const mutedColor = useMemo(() => {
    const bg = cssVar("--bg-secondary") || "#28324f";
    const barMutedColor = color(bg);
    return isLight ? barMutedColor.darken(10).toHexString() : barMutedColor.lighten(10).toHexString();
  }, [isLight]);
  const getBarColor = useColorFunction(
    totalColorTicks,
    totalTicksRight,
    diffMode,
    colorScheme,
    isLight,
    mutedColor,
    rangeMin,
    rangeMax,
    matchedLabels,
    focusedItemData ? focusedItemData.item.level : 0
  );
  const renderFunc = useRenderFunc(
    ctx,
    data,
    getBarColor,
    textAlign,
    collapsedMap,
    diffMode,
    totalColorTicks,
    totalTicksRight
  );
  useEffect(() => {
    if (!ctx) {
      return;
    }
    ctx.clearRect(0, 0, ctx.canvas.width, ctx.canvas.height);
    const mutedPath2D = new Path2D();
    walkTree(
      root,
      direction,
      data,
      totalViewTicks,
      rangeMin,
      rangeMax,
      wrapperWidth,
      collapsedMap,
      (item, x, y, width, height, label, muted) => {
        if (muted) {
          mutedPath2D.rect(x, y, width, height);
        } else {
          renderFunc(item, x, y, width, height, label);
        }
      }
    );
    ctx.fillStyle = mutedColor;
    ctx.fill(mutedPath2D);
  }, [
    ctx,
    data,
    root,
    wrapperWidth,
    rangeMin,
    rangeMax,
    totalViewTicks,
    direction,
    renderFunc,
    collapsedMap,
    mutedColor
  ]);
}
function useRenderFunc(ctx, data, getBarColor, textAlign, collapsedMap, diffMode, totalColorTicks, totalTicksRight) {
  return useMemo(() => {
    if (!ctx) {
      return () => {
      };
    }
    const renderFunc = (item, x, y, width, height, label) => {
      ctx.beginPath();
      ctx.rect(x + BAR_BORDER_WIDTH, y, width, height);
      ctx.fillStyle = getBarColor(item, label, false);
      ctx.stroke();
      ctx.fill();
      const collapsedItemConfig = collapsedMap.get(item);
      let finalLabel = label;
      if (collapsedItemConfig && collapsedItemConfig.collapsed) {
        const numberOfCollapsedItems = collapsedItemConfig.items.length;
        finalLabel = `(${numberOfCollapsedItems}) ` + label;
      }
      if (width >= LABEL_THRESHOLD) {
        if (collapsedItemConfig) {
          renderLabel(
            ctx,
            data,
            finalLabel,
            item,
            width,
            textAlign === "left" ? x + GROUP_STRIP_MARGIN_LEFT + GROUP_TEXT_OFFSET : x,
            y,
            textAlign,
            diffMode === "proportionalDiff" && totalTicksRight !== void 0 ? getProportionalLabel(item, totalColorTicks, totalTicksRight) : void 0
          );
          renderGroupingStrip(ctx, x, y, height, item, collapsedItemConfig);
        } else {
          renderLabel(
            ctx,
            data,
            finalLabel,
            item,
            width,
            x,
            y,
            textAlign,
            diffMode === "proportionalDiff" && totalTicksRight !== void 0 ? getProportionalLabel(item, totalColorTicks, totalTicksRight) : void 0
          );
        }
      }
    };
    return renderFunc;
  }, [
    ctx,
    getBarColor,
    textAlign,
    data,
    collapsedMap,
    diffMode,
    totalColorTicks,
    totalTicksRight
  ]);
}
function renderGroupingStrip(ctx, x, y, height, item, collapsedItemConfig) {
  const groupStripX = x + GROUP_STRIP_MARGIN_LEFT;
  ctx.beginPath();
  ctx.rect(
    x,
    y,
    groupStripX - x + GROUP_STRIP_WIDTH + GROUP_STRIP_PADDING,
    height
  );
  ctx.fill();
  ctx.beginPath();
  if (collapsedItemConfig.collapsed) {
    ctx.rect(groupStripX, y + height / 4, GROUP_STRIP_WIDTH, height / 2);
  } else {
    if (collapsedItemConfig.items[0] === item) {
      ctx.rect(groupStripX, y + height / 2, GROUP_STRIP_WIDTH, height / 2);
    } else if (collapsedItemConfig.items[collapsedItemConfig.items.length - 1] === item) {
      ctx.rect(groupStripX, y, GROUP_STRIP_WIDTH, height / 2);
    } else {
      ctx.rect(groupStripX, y, GROUP_STRIP_WIDTH, height);
    }
  }
  ctx.fillStyle = "#666";
  ctx.fill();
}
function walkTree(root, direction, data, totalViewTicks, rangeMin, rangeMax, wrapperWidth, collapsedMap, renderFunc) {
  const stack = [];
  stack.push({ item: root, levelOffset: 0 });
  const pixelsPerTick = wrapperWidth * window.devicePixelRatio / totalViewTicks / (rangeMax - rangeMin);
  let collapsedItemRendered;
  while (stack.length > 0) {
    const { item, levelOffset } = stack.shift();
    const curBarTicks = item.value;
    const muted = curBarTicks * pixelsPerTick <= MUTE_THRESHOLD;
    const width = curBarTicks * pixelsPerTick - (muted ? 0 : BAR_BORDER_WIDTH * 2);
    const height = PIXELS_PER_LEVEL;
    if (width < HIDE_THRESHOLD) {
      continue;
    }
    let offsetModifier = 0;
    let skipRender = false;
    const collapsedItemConfig = collapsedMap.get(item);
    const isCollapsedItem = collapsedItemConfig && collapsedItemConfig.collapsed;
    if (isCollapsedItem) {
      if (collapsedItemRendered === collapsedItemConfig.items[0]) {
        offsetModifier = direction === "children" ? -1 : 1;
        skipRender = true;
      } else {
        collapsedItemRendered = void 0;
      }
    } else {
      collapsedItemRendered = void 0;
    }
    if (!skipRender) {
      const barX = getBarX(item.start, totalViewTicks, rangeMin, pixelsPerTick);
      const barY = (item.level + levelOffset) * PIXELS_PER_LEVEL;
      const label = data.getLabel(item.itemIndexes[0]);
      if (isCollapsedItem) {
        collapsedItemRendered = item;
      }
      renderFunc(item, barX, barY, width, height, label, muted);
    }
    const nextList = direction === "children" ? item.children : item.parents;
    if (nextList) {
      stack.unshift(
        ...nextList.map((c) => ({
          item: c,
          levelOffset: levelOffset + offsetModifier
        }))
      );
    }
  }
}
function useColorFunction(totalTicks, totalTicksRight, diffMode, colorScheme, isLight, mutedColor, rangeMin, rangeMax, matchedLabels, topLevel) {
  return useCallback(
    function getColor(item, label, muted) {
      if (muted && !matchedLabels) {
        return mutedColor;
      }
      const barColor = totalTicksRight !== void 0 ? color(
        getBarColorByDiff(
          item.value,
          item.valueRight ?? 0,
          totalTicks,
          totalTicksRight,
          colorScheme === "diffColorBlind" ? "diffColorBlind" : "default",
          diffMode
        )
      ) : colorScheme === ColorScheme.ValueBased ? getBarColorByValue(item.value, totalTicks, rangeMin, rangeMax) : getBarColorByPackage(label, isLight);
      if (matchedLabels) {
        return matchedLabels.has(label) ? barColor.toHslString() : mutedColor;
      }
      return item.level > topLevel - 1 ? barColor.toHslString() : barColor.lighten(15).toHslString();
    },
    [
      totalTicks,
      totalTicksRight,
      diffMode,
      colorScheme,
      isLight,
      rangeMin,
      rangeMax,
      matchedLabels,
      topLevel,
      mutedColor
    ]
  );
}
function useSetupCanvas(canvasRef, wrapperWidth, numberOfLevels) {
  const [ctx, setCtx] = useState();
  useEffect(() => {
    if (!(numberOfLevels && canvasRef.current)) {
      return;
    }
    const ctx2 = canvasRef.current.getContext("2d");
    const height = PIXELS_PER_LEVEL * numberOfLevels;
    canvasRef.current.width = Math.round(
      wrapperWidth * window.devicePixelRatio
    );
    canvasRef.current.height = Math.round(height);
    canvasRef.current.style.width = `${wrapperWidth}px`;
    canvasRef.current.style.height = `${height / window.devicePixelRatio}px`;
    ctx2.textBaseline = "middle";
    ctx2.font = 12 * window.devicePixelRatio + "px monospace";
    ctx2.strokeStyle = "white";
    setCtx(ctx2);
  }, [canvasRef, setCtx, wrapperWidth, numberOfLevels]);
  return ctx;
}
function getProportionalLabel(item, totalTicks, totalRight) {
  const left = item.value - (item.valueRight ?? 0);
  const leftTotal = totalTicks - totalRight;
  const baselineShare = leftTotal ? left / leftTotal : 0;
  const comparisonShare = totalRight ? (item.valueRight ?? 0) / totalRight : 0;
  if (!baselineShare) return comparisonShare ? "new" : "0.00%";
  const change = (comparisonShare - baselineShare) / baselineShare * 100;
  return `${change > 0 ? "+" : ""}${change.toFixed(2)}%`;
}
function renderLabel(ctx, data, label, item, width, x, y, textAlign, proportionalLabel) {
  ctx.save();
  ctx.clip();
  ctx.fillStyle = "#222";
  const displayValue = data.valueDisplayProcessor(item.value);
  const unit = displayValue.suffix ? displayValue.text + displayValue.suffix : displayValue.text;
  const spaceForTextInRect = width - BAR_TEXT_PADDING_LEFT;
  let fullLabel = `${label} (${proportionalLabel ?? unit})`;
  let labelX = Math.max(x, 0) + BAR_TEXT_PADDING_LEFT;
  if (ctx.measureText(proportionalLabel ? fullLabel : label).width > spaceForTextInRect) {
    if (proportionalLabel) fullLabel = `(${proportionalLabel}) ${label}`;
    ctx.textAlign = textAlign;
    if (textAlign === "right") {
      fullLabel = proportionalLabel ? `${label} (${proportionalLabel})` : label;
      labelX = x + width - BAR_TEXT_PADDING_LEFT;
    }
  }
  ctx.fillText(fullLabel, labelX, y + PIXELS_PER_LEVEL / 2 + 2);
  ctx.restore();
}
function getBarX(offset, totalTicks, rangeMin, pixelsPerTick) {
  return (offset - totalTicks * rangeMin) * pixelsPerTick;
}

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
  totalViewTicks,
  totalProfileTicks,
  totalProfileTicksRight,
  diffMode,
  root,
  direction,
  depth,
  showFlameGraphOnly,
  collapsedMap,
  setCollapsedMap,
  collapsing,
  getExtraContextMenuButtons,
  selectedView,
  search
}) => {
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
    totalColorTicks: totalProfileTicks ?? totalViewTicks,
    totalTicksRight: totalProfileTicksRight,
    diffMode,
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
    [
      data,
      rangeMin,
      rangeMax,
      totalViewTicks,
      root,
      direction,
      depth,
      collapsedMap
    ]
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
    [
      rangeMin,
      rangeMax,
      totalViewTicks,
      clickedItemData,
      setMousePosition,
      root,
      direction,
      depth,
      collapsedMap
    ]
  );
  const onGraphMouseLeave = useCallback(() => {
    setTooltipItem(void 0);
  }, []);
  useEffect(() => {
    const handleOnClick = (e) => {
      if (e.target instanceof HTMLElement && e.target.parentElement?.id !== "flameGraphCanvasContainer_clickOutsideCheck") {
        setClickedItemData(void 0);
      }
    };
    window.addEventListener("click", handleOnClick);
    return () => window.removeEventListener("click", handleOnClick);
  }, [setClickedItemData]);
  return /* @__PURE__ */ jsxs("div", { className: "fg-canvas-graph", children: [
    /* @__PURE__ */ jsx(
      "div",
      {
        className: "fg-canvas-wrapper",
        id: "flameGraphCanvasContainer_clickOutsideCheck",
        ref: sizeRef,
        children: /* @__PURE__ */ jsx(
          "canvas",
          {
            ref: graphRef,
            "data-testid": "flameGraph",
            onClick: onGraphClick,
            onMouseMove: onGraphMouseMove,
            onMouseLeave: onGraphMouseLeave
          }
        )
      }
    ),
    /* @__PURE__ */ jsx(
      FlameGraphTooltip,
      {
        position: mousePosition,
        item: tooltipItem,
        data,
        totalTicks: totalProfileTicks ?? totalViewTicks,
        diffMode,
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
          const expanded = collapsedMap.expandForFocus(clickedItemData.item);
          if (expanded !== collapsedMap) setCollapsedMap(expanded);
          setRangeMin(clickedItemData.item.start / totalViewTicks);
          setRangeMax(
            (clickedItemData.item.start + clickedItemData.item.value) / totalViewTicks
          );
          onItemFocused(clickedItemData);
        },
        onSandwich: () => {
          onSandwich(data.getLabel(clickedItemData.item.itemIndexes[0]));
        },
        onExpandGroup: () => {
          setCollapsedMap(
            collapsedMap.setCollapsedStatus(clickedItemData.item, false)
          );
        },
        onCollapseGroup: () => {
          setCollapsedMap(
            collapsedMap.setCollapsedStatus(clickedItemData.item, true)
          );
        },
        onExpandAllGroups: () => {
          setCollapsedMap(collapsedMap.setAllCollapsedStatus(false));
        },
        onCollapseAllGroups: () => {
          setCollapsedMap(collapsedMap.setAllCollapsedStatus(true));
        },
        allGroupsCollapsed: Array.from(collapsedMap.values()).every(
          (i) => i.collapsed
        ),
        allGroupsExpanded: Array.from(collapsedMap.values()).every(
          (i) => !i.collapsed
        ),
        getExtraContextMenuButtons,
        selectedView,
        search
      }
    )
  ] });
};
const convertPixelCoordinatesToBarCoordinates = (pos, root, direction, depth, pixelsPerTick, totalTicks, rangeMin, collapsedMap) => {
  let next = root;
  let currentLevel = direction === "children" ? 0 : depth - 1;
  const levelIndex = Math.floor(
    pos.y / (PIXELS_PER_LEVEL / window.devicePixelRatio)
  );
  let found;
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
      const xEnd = getBarX(
        child.start + child.value,
        totalTicks,
        rangeMin,
        pixelsPerTick
      );
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

const FlameGraphMetadata = memo(
  ({
    data,
    focusedItem,
    totalTicks,
    sandwichedLabel,
    onFocusPillClick,
    onSandwichPillClick
  }) => {
    const parts = [];
    const ticksVal = formatShort(totalTicks);
    const displayValue = data.valueDisplayProcessor(totalTicks);
    let unitValue = displayValue.text + displayValue.suffix;
    const unitTitle = data.getUnitTitle();
    if (unitTitle === "Count") {
      if (!displayValue.suffix) {
        unitValue = displayValue.text;
      }
    }
    parts.push(
      /* @__PURE__ */ jsxs("div", { className: "fg-metadata-pill", children: [
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
        /* @__PURE__ */ jsxs(
          "div",
          {
            title: sandwichedLabel,
            className: "fg-metadata-pill-group",
            children: [
              /* @__PURE__ */ jsx(Icon, { size: 12, name: "angle-right" }),
              /* @__PURE__ */ jsxs("div", { className: "fg-metadata-pill", children: [
                /* @__PURE__ */ jsx(Icon, { size: 12, name: "sandwich" }),
                /* @__PURE__ */ jsx("span", { className: "fg-metadata-pill-name", children: sandwichedLabel.substring(sandwichedLabel.lastIndexOf("/") + 1) }),
                /* @__PURE__ */ jsx(
                  PillCloseButton,
                  {
                    onClick: onSandwichPillClick,
                    label: "Remove sandwich view"
                  }
                )
              ] })
            ]
          },
          "sandwich"
        )
      );
    }
    if (focusedItem) {
      const percentValue = totalTicks > 0 ? Math.round(1e4 * (focusedItem.item.value / totalTicks)) / 100 : 0;
      const iconName = percentValue > 0 ? "eye" : "exclamation-circle";
      parts.push(
        /* @__PURE__ */ jsxs(
          "div",
          {
            title: focusedItem.label,
            className: "fg-metadata-pill-group",
            children: [
              /* @__PURE__ */ jsx(Icon, { size: 12, name: "angle-right" }),
              /* @__PURE__ */ jsxs("div", { className: "fg-metadata-pill", children: [
                /* @__PURE__ */ jsx(Icon, { size: 12, name: iconName }),
                "\xA0",
                percentValue,
                "% of total",
                /* @__PURE__ */ jsx(PillCloseButton, { onClick: onFocusPillClick, label: "Remove focus" })
              ] })
            ]
          },
          "focus"
        )
      );
    }
    return /* @__PURE__ */ jsx("div", { className: "fg-metadata", children: parts });
  }
);
FlameGraphMetadata.displayName = "FlameGraphMetadata";
function PillCloseButton({
  onClick,
  label
}) {
  return /* @__PURE__ */ jsx(
    "button",
    {
      type: "button",
      className: "fg-metadata-pill-close",
      onClick,
      "aria-label": label,
      title: label,
      children: /* @__PURE__ */ jsx(Icon, { name: "times", size: 12 })
    }
  );
}

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
  diffMode = "proportionalDiff",
  showFlameGraphOnly,
  getExtraContextMenuButtons,
  collapsing,
  search,
  collapsedMap,
  setCollapsedMap,
  selectedView
}) => {
  const sandwichMargin = `${PIXELS_PER_LEVEL / window.devicePixelRatio}px`;
  const [levels, setLevels] = useState();
  const [levelsCallers, setLevelsCallers] = useState();
  const [totalViewTicks, setTotalViewTicks] = useState(0);
  const totalProfileTicks = data.getLevels()[0]?.[0]?.value ?? 0;
  const totalProfileTicksRight = data.isDiffFlamegraph() ? data.getLevels()[0]?.[0]?.valueRight : void 0;
  useEffect(() => {
    if (data) {
      let levels2 = data.getLevels();
      let totalViewTicks2 = levels2.length ? levels2[0][0].value : 0;
      let levelsCallers2;
      if (sandwichItem) {
        const [callers, callees] = data.getSandwichLevels(sandwichItem);
        levels2 = callees;
        levelsCallers2 = callers;
        totalViewTicks2 = callees[0]?.[0]?.value ?? 0;
      }
      setLevels(levels2);
      setLevelsCallers(levelsCallers2);
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
    diffMode,
    totalViewTicks,
    totalProfileTicks,
    totalProfileTicksRight,
    showFlameGraphOnly,
    collapsedMap,
    setCollapsedMap,
    getExtraContextMenuButtons,
    collapsing,
    search,
    selectedView
  };
  let canvas = null;
  if (levelsCallers?.length) {
    canvas = /* @__PURE__ */ jsxs(Fragment, { children: [
      /* @__PURE__ */ jsxs(
        "div",
        {
          className: "fg-sandwich-canvas-wrapper",
          style: { marginBottom: sandwichMargin },
          children: [
            /* @__PURE__ */ jsxs("div", { className: "fg-sandwich-marker", children: [
              "Callers",
              /* @__PURE__ */ jsx(Icon, { className: "fg-sandwich-marker-icon", name: "angle-down" })
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
          ]
        }
      ),
      /* @__PURE__ */ jsxs(
        "div",
        {
          className: "fg-sandwich-canvas-wrapper",
          style: { marginBottom: sandwichMargin },
          children: [
            /* @__PURE__ */ jsxs(
              "div",
              {
                className: cx("fg-sandwich-marker", "fg-sandwich-marker-callees"),
                children: [
                  /* @__PURE__ */ jsx(Icon, { className: "fg-sandwich-marker-icon", name: "angle-up" }),
                  "Callees"
                ]
              }
            ),
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
          ]
        }
      )
    ] });
  } else if (levels?.length) {
    canvas = /* @__PURE__ */ jsx(
      FlameGraphCanvas,
      {
        ...commonCanvasProps,
        root: levels[0][0],
        depth: levels.length,
        direction: "children"
      }
    );
  }
  return /* @__PURE__ */ jsxs("div", { className: "fg-graph", children: [
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
    canvas,
    data.isDiffFlamegraph() && /* @__PURE__ */ jsxs(
      "div",
      {
        className: "fg-diff-legend",
        role: "note",
        "aria-label": `${diffMode === "absoluteDiff" ? "Absolute" : "Proportional"} diff flamegraph color legend`,
        children: [
          /* @__PURE__ */ jsx("strong", { children: diffMode === "absoluteDiff" ? "Absolute diff" : "Proportional diff" }),
          /* @__PURE__ */ jsx(
            "div",
            {
              className: "fg-diff-legend-scale",
              style: {
                background: colorScheme === "diffColorBlind" ? diffColorBlindGradient : diffDefaultGradient
              },
              "aria-hidden": "true"
            }
          ),
          /* @__PURE__ */ jsxs("div", { className: "fg-diff-legend-labels", children: [
            /* @__PURE__ */ jsxs("span", { children: [
              colorScheme === "diffColorBlind" ? "Blue" : "Green",
              ": decreased"
            ] }),
            /* @__PURE__ */ jsx("span", { children: "Gray: unchanged" }),
            /* @__PURE__ */ jsxs("span", { children: [
              colorScheme === "diffColorBlind" ? "Orange-red" : "Red",
              ": increased"
            ] })
          ] }),
          /* @__PURE__ */ jsx("small", { children: diffMode === "absoluteDiff" ? "Color shows the raw sample count change (comparison minus baseline); intensity is scaled to the larger profile\u2019s total. Bar width is the combined total." : "Node labels show the percentage change in each function\u2019s share of the profile from baseline to comparison (or \u201Cnew\u201D if its baseline share was zero). Color shows the same change, not the change in raw samples. Bar width is the combined total." })
        ]
      }
    )
  ] });
};

function Popover({
  trigger,
  overlay
}) {
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState(null);
  const anchorRef = useRef(null);
  const overlayRef = useRef(null);
  useEffect(() => {
    if (!open) return;
    const onDocClick = (e) => {
      const target = e.target;
      if (overlayRef.current?.contains(target)) return;
      if (anchorRef.current?.contains(target)) return;
      setOpen(false);
    };
    const onKey = (e) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDocClick);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDocClick);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  useEffect(() => {
    if (!open || !anchorRef.current) return;
    const rect = anchorRef.current.getBoundingClientRect();
    const wantsTop = rect.bottom + 240 > window.innerHeight && rect.top - 240 > 0;
    setPos({
      left: rect.left,
      top: wantsTop ? rect.top - 4 : rect.bottom + 4
    });
  }, [open]);
  const toggle = () => setOpen((o) => !o);
  const close = () => setOpen(false);
  return /* @__PURE__ */ jsxs(Fragment, { children: [
    /* @__PURE__ */ jsx("span", { ref: anchorRef, className: "fg-popover-anchor", children: trigger({ open, toggle }) }),
    open && pos ? createPortal(
      /* @__PURE__ */ jsx(
        "div",
        {
          ref: overlayRef,
          className: "fg-popover-overlay",
          style: pos,
          role: "menu",
          children: overlay({ close })
        }
      ),
      document.body
    ) : null
  ] });
}
function PopoverItem({
  label,
  onClick,
  active
}) {
  return /* @__PURE__ */ jsx(
    "div",
    {
      role: "menuitem",
      tabIndex: 0,
      className: "fg-popover-item",
      "data-active": active ?? false,
      onClick,
      onKeyDown: (e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onClick();
        }
      },
      children: label
    }
  );
}

function ColorSchemeButton(props) {
  const gradient = props.isDiffMode ? props.value === ColorSchemeDiff.DiffColorBlind ? diffColorBlindGradient : diffDefaultGradient : props.value === ColorScheme.PackageBased ? byPackageGradient : byValueGradient;
  return /* @__PURE__ */ jsx(
    Popover,
    {
      trigger: ({ toggle }) => /* @__PURE__ */ jsx(
        "button",
        {
          type: "button",
          className: "fg-cs-button",
          onClick: toggle,
          "aria-label": "Change color scheme",
          title: "Change color scheme",
          children: /* @__PURE__ */ jsx("span", { className: "fg-cs-dot", style: { background: gradient } })
        }
      ),
      overlay: ({ close }) => /* @__PURE__ */ jsx(Fragment, { children: props.isDiffMode ? /* @__PURE__ */ jsxs(Fragment, { children: [
        /* @__PURE__ */ jsx(
          PopoverItem,
          {
            label: "Diff",
            active: props.value === ColorSchemeDiff.Default,
            onClick: () => {
              props.onChange(ColorSchemeDiff.Default);
              close();
            }
          }
        ),
        /* @__PURE__ */ jsx(
          PopoverItem,
          {
            label: "Diff (color blind)",
            active: props.value === ColorSchemeDiff.DiffColorBlind,
            onClick: () => {
              props.onChange(ColorSchemeDiff.DiffColorBlind);
              close();
            }
          }
        )
      ] }) : /* @__PURE__ */ jsxs(Fragment, { children: [
        /* @__PURE__ */ jsx(
          PopoverItem,
          {
            label: "By package name",
            active: props.value === ColorScheme.PackageBased,
            onClick: () => {
              props.onChange(ColorScheme.PackageBased);
              close();
            }
          }
        ),
        /* @__PURE__ */ jsx(
          PopoverItem,
          {
            label: "By value",
            active: props.value === ColorScheme.ValueBased,
            onClick: () => {
              props.onChange(ColorScheme.ValueBased);
              close();
            }
          }
        )
      ] }) })
    }
  );
}

const FlameGraphHeader = (props) => {
  const [localSearch, setLocalSearch] = useSearchInput(
    props.search,
    props.setSearch
  );
  const {
    onReset,
    textAlign,
    onTextAlignChange,
    showResetButton,
    colorScheme,
    onColorSchemeChange,
    stickyHeader,
    extraHeaderElements,
    setCollapsedMap,
    collapsedMap
  } = props;
  return /* @__PURE__ */ jsxs("div", { className: cx("fg-header", { "fg-header-sticky": stickyHeader }), children: [
    /* @__PURE__ */ jsx("div", { className: "fg-header-input-container", children: /* @__PURE__ */ jsxs("div", { className: "fg-header-search-wrapper", children: [
      /* @__PURE__ */ jsx(
        "input",
        {
          type: "text",
          className: "fg-header-search-input",
          value: localSearch || "",
          onChange: (e) => setLocalSearch(e.currentTarget.value),
          placeholder: "Search..."
        }
      ),
      localSearch !== "" ? /* @__PURE__ */ jsxs(
        "button",
        {
          type: "button",
          className: "fg-header-clear-button",
          onClick: () => {
            props.setSearch("");
            setLocalSearch("");
          },
          "aria-label": "Clear",
          children: [
            /* @__PURE__ */ jsx(Icon, { name: "times", size: 12 }),
            /* @__PURE__ */ jsx("span", { children: "Clear" })
          ]
        }
      ) : null
    ] }) }),
    /* @__PURE__ */ jsxs("div", { className: "fg-header-right", children: [
      showResetButton && /* @__PURE__ */ jsx(
        IconBtn,
        {
          icon: "history-alt",
          label: "Reset focus and sandwich state",
          onClick: onReset
        }
      ),
      /* @__PURE__ */ jsxs("div", { className: "fg-header-control fg-header-spacing-right", children: [
        /* @__PURE__ */ jsx("span", { children: "Colors" }),
        /* @__PURE__ */ jsx(
          ColorSchemeButton,
          {
            value: colorScheme,
            onChange: onColorSchemeChange,
            isDiffMode: props.isDiffMode
          }
        )
      ] }),
      props.viewMode && props.setViewMode && /* @__PURE__ */ jsxs("div", { className: "fg-layout-picker fg-header-spacing-right", children: [
        /* @__PURE__ */ jsx("span", { children: "Layout" }),
        /* @__PURE__ */ jsxs("div", { className: "fg-segmented", role: "group", "aria-label": "Layout", children: [
          /* @__PURE__ */ jsx(
            "button",
            {
              type: "button",
              title: "Show one visualization pane",
              "aria-pressed": props.viewMode === "single",
              onClick: () => props.setViewMode?.("single"),
              children: "Single"
            }
          ),
          /* @__PURE__ */ jsx(
            "button",
            {
              type: "button",
              title: "Show two independently configured visualization panes",
              "aria-pressed": props.viewMode === "dual",
              onClick: () => props.setViewMode?.("dual"),
              children: "Dual"
            }
          )
        ] })
      ] }),
      /* @__PURE__ */ jsxs("div", { className: "fg-header-control fg-header-spacing-right", children: [
        /* @__PURE__ */ jsx("span", { children: "Grouping" }),
        /* @__PURE__ */ jsxs("div", { className: "fg-header-btn-group", children: [
          /* @__PURE__ */ jsx(
            IconBtn,
            {
              icon: "angle-double-down",
              label: "Expand all groups",
              disabled: props.disableCollapsing || collapsedMap.size() === 0,
              onClick: () => setCollapsedMap(collapsedMap.setAllCollapsedStatus(false)),
              grouped: true
            }
          ),
          /* @__PURE__ */ jsx(
            IconBtn,
            {
              icon: "angle-double-up",
              label: "Collapse all groups",
              disabled: props.disableCollapsing || collapsedMap.size() === 0,
              onClick: () => setCollapsedMap(collapsedMap.setAllCollapsedStatus(true)),
              grouped: true
            }
          )
        ] })
      ] }),
      /* @__PURE__ */ jsxs("div", { className: "fg-header-radio-control fg-header-spacing-right", children: [
        /* @__PURE__ */ jsx("span", { children: "Text alignment" }),
        /* @__PURE__ */ jsx(
          RadioGroup,
          {
            name: "text-align",
            value: textAlign,
            onChange: onTextAlignChange,
            options: [
              {
                value: "left",
                title: "Align clipped text left",
                icon: "align-left"
              },
              {
                value: "right",
                title: "Align clipped text right",
                icon: "align-right"
              }
            ]
          }
        )
      ] }),
      extraHeaderElements && /* @__PURE__ */ jsx("div", { className: "fg-header-extra-elements", children: extraHeaderElements })
    ] })
  ] });
};
function IconBtn({
  icon,
  label,
  onClick,
  disabled,
  grouped
}) {
  return /* @__PURE__ */ jsx(
    "button",
    {
      type: "button",
      className: cx(
        "fg-header-icon-btn",
        grouped && "fg-header-icon-btn-grouped",
        !grouped && "fg-header-spacing-right"
      ),
      onClick,
      disabled,
      "aria-label": label,
      title: label,
      children: /* @__PURE__ */ jsx(Icon, { name: icon, size: 14 })
    }
  );
}
function RadioGroup({
  name,
  value,
  onChange,
  options,
  disabled,
  className
}) {
  const groupId = useId();
  return /* @__PURE__ */ jsx("div", { className: cx("fg-header-radio-group", className), role: "radiogroup", children: options.map((opt) => {
    const id = `${groupId}-${opt.value}`;
    const checked = value === opt.value;
    return /* @__PURE__ */ jsxs("span", { className: "fg-header-radio-cell", children: [
      /* @__PURE__ */ jsx(
        "input",
        {
          type: "radio",
          id,
          name: `${name}-${groupId}`,
          checked,
          disabled,
          onChange: () => onChange(opt.value),
          className: "fg-header-radio-input",
          "aria-label": opt.title
        }
      ),
      /* @__PURE__ */ jsxs(
        "label",
        {
          htmlFor: id,
          title: opt.title,
          className: "fg-header-radio-label",
          "data-checked": checked,
          children: [
            opt.icon && /* @__PURE__ */ jsx(Icon, { name: opt.icon, size: 14 }),
            opt.label && /* @__PURE__ */ jsx("span", { children: opt.label })
          ]
        }
      )
    ] }, opt.value);
  }) });
}
function useSearchInput(search, setSearch) {
  const [localSearchState, setLocalSearchState] = useState(search);
  const prevSearch = usePrevious(search);
  useDebounce(
    () => {
      setSearch(localSearchState);
    },
    250,
    [localSearchState]
  );
  useEffect(() => {
    if (prevSearch !== search && search !== localSearchState) {
      setLocalSearchState(search);
    }
  }, [search, prevSearch, localSearchState]);
  return [localSearchState, setLocalSearchState];
}

const ROW_HEIGHT = 25;
const OVERSCAN_ROWS = 8;
const HEADER_HEIGHT = 27;
const FlameGraphTopTableContainer = memo(
  ({
    data,
    diffMode,
    colorScheme,
    onSymbolClick,
    search,
    matchedLabels,
    onSearch,
    sandwichItem,
    onSandwich,
    onTableSort
  }) => {
    const isLight = useIsLight();
    const rows = useMemo(() => {
      const grouped = buildFilteredTable(data, matchedLabels);
      return Object.entries(grouped).map(([symbol, v]) => ({
        symbol,
        self: v.self ?? 0,
        total: (v.total ?? 0) + (v.totalRight ?? 0),
        totalRight: v.totalRight ?? 0
      }));
    }, [data, matchedLabels]);
    const [sort, setSort] = useState({
      column: data.isDiffFlamegraph() ? "Baseline" : "Self",
      direction: "desc"
    });
    const sortedRows = useMemo(() => {
      const dir = sort.direction === "asc" ? 1 : -1;
      const copy = rows.slice();
      copy.sort((a, b) => {
        if (sort.column === "Symbol")
          return a.symbol.localeCompare(b.symbol) * dir;
        const leftTotal = data.getValue(0), rightTotal = data.getValueRight(0);
        const metric = (row) => {
          const left = row.total - row.totalRight;
          const right = row.totalRight;
          if (sort.column === "Self") return row.self;
          if (sort.column === "Comparison")
            return diffMode === "absoluteDiff" ? right : rightTotal ? right / rightTotal : 0;
          if (sort.column === "Baseline")
            return diffMode === "absoluteDiff" ? left : leftTotal ? left / leftTotal : 0;
          if (sort.column === "Diff") {
            if (diffMode === "absoluteDiff") return right - left;
            const baseline = leftTotal ? left / leftTotal : 0;
            const comparison = rightTotal ? right / rightTotal : 0;
            return baseline ? (comparison - baseline) / baseline : comparison ? 1 : 0;
          }
          return row.total;
        };
        const av = metric(a);
        const bv = metric(b);
        return (av - bv) * dir;
      });
      return copy;
    }, [rows, sort, data, diffMode]);
    const handleSort = useCallback(
      (column) => {
        setSort((prev) => {
          const next = prev.column === column ? {
            column,
            direction: prev.direction === "desc" ? "asc" : "desc"
          } : { column, direction: column === "Symbol" ? "asc" : "desc" };
          onTableSort?.(`${next.column}_${next.direction}`);
          return next;
        });
      },
      [onTableSort]
    );
    const scrollRef = useRef(null);
    const [scrollTop, setScrollTop] = useState(0);
    const [viewportH, setViewportH] = useState(600);
    useEffect(() => {
      const el = scrollRef.current;
      if (!el) return;
      const onScroll = () => setScrollTop(el.scrollTop);
      el.addEventListener("scroll", onScroll, { passive: true });
      const ro = new ResizeObserver((entries) => {
        for (const entry of entries) setViewportH(entry.contentRect.height);
      });
      ro.observe(el);
      setViewportH(el.clientHeight);
      return () => {
        el.removeEventListener("scroll", onScroll);
        ro.disconnect();
      };
    }, []);
    const visibleBodyH = Math.max(0, viewportH - HEADER_HEIGHT);
    const firstVisible = Math.max(
      0,
      Math.floor((scrollTop - HEADER_HEIGHT) / ROW_HEIGHT)
    );
    const lastVisible = Math.min(
      sortedRows.length,
      Math.ceil((scrollTop + visibleBodyH) / ROW_HEIGHT) + 1
    );
    const startIdx = Math.max(0, firstVisible - OVERSCAN_ROWS);
    const endIdx = Math.min(sortedRows.length, lastVisible + OVERSCAN_ROWS);
    const padTop = startIdx * ROW_HEIGHT;
    const padBottom = (sortedRows.length - endIdx) * ROW_HEIGHT;
    return /* @__PURE__ */ jsx(
      "div",
      {
        className: `fg-tt-container${data.isDiffFlamegraph() ? " fg-tt-diff" : ""}`,
        "data-testid": "topTable",
        children: /* @__PURE__ */ jsx("div", { ref: scrollRef, className: "fg-tt-scroll", children: /* @__PURE__ */ jsxs("table", { className: "fg-tt-table", role: "table", children: [
          /* @__PURE__ */ jsx("thead", { className: "fg-tt-thead", children: /* @__PURE__ */ jsxs("tr", { role: "row", className: "fg-tt-header-row", children: [
            /* @__PURE__ */ jsx(
              "th",
              {
                "aria-label": "Row actions",
                className: "fg-tt-action-header"
              }
            ),
            /* @__PURE__ */ jsx(
              SortHeader,
              {
                column: "Symbol",
                active: sort.column === "Symbol",
                direction: sort.direction,
                align: "left",
                onClick: handleSort,
                className: "fg-tt-symbol-header"
              }
            ),
            data.isDiffFlamegraph() ? ["Baseline", "Comparison", "Diff"].map(
              (column) => /* @__PURE__ */ jsx(
                SortHeader,
                {
                  column,
                  active: sort.column === column,
                  direction: sort.direction,
                  align: "right",
                  onClick: handleSort,
                  className: "fg-tt-numeric-header"
                },
                column
              )
            ) : /* @__PURE__ */ jsxs(Fragment, { children: [
              /* @__PURE__ */ jsx(
                SortHeader,
                {
                  column: "Self",
                  active: sort.column === "Self",
                  direction: sort.direction,
                  align: "right",
                  onClick: handleSort,
                  className: "fg-tt-numeric-header"
                }
              ),
              /* @__PURE__ */ jsx(
                SortHeader,
                {
                  column: "Total",
                  active: sort.column === "Total",
                  direction: sort.direction,
                  align: "right",
                  onClick: handleSort,
                  className: "fg-tt-numeric-header"
                }
              )
            ] })
          ] }) }),
          /* @__PURE__ */ jsxs("tbody", { children: [
            padTop > 0 && /* @__PURE__ */ jsx("tr", { "aria-hidden": "true", style: { height: padTop }, children: /* @__PURE__ */ jsx("td", { colSpan: data.isDiffFlamegraph() ? 5 : 4 }) }),
            sortedRows.slice(startIdx, endIdx).map((row) => /* @__PURE__ */ jsx(
              TableRow,
              {
                data,
                diffMode,
                colorScheme,
                isLight,
                row,
                search,
                sandwichItem,
                onSymbolClick,
                onSearch,
                onSandwich
              },
              row.symbol
            )),
            padBottom > 0 && /* @__PURE__ */ jsx("tr", { "aria-hidden": "true", style: { height: padBottom }, children: /* @__PURE__ */ jsx("td", { colSpan: data.isDiffFlamegraph() ? 5 : 4 }) })
          ] })
        ] }) })
      }
    );
  }
);
FlameGraphTopTableContainer.displayName = "FlameGraphTopTableContainer";
function SortHeaderInner({
  column,
  active,
  direction,
  align,
  onClick,
  className
}) {
  const label = `Sort by column ${column}${active ? direction === "desc" ? ", descending" : ", ascending" : ""}`;
  const indicator = active ? /* @__PURE__ */ jsx(Icon, { name: direction === "desc" ? "angle-down" : "angle-up", size: 12 }) : null;
  return /* @__PURE__ */ jsx(
    "th",
    {
      className,
      "aria-sort": active ? direction === "desc" ? "descending" : "ascending" : "none",
      children: /* @__PURE__ */ jsxs(
        "button",
        {
          type: "button",
          className: "fg-tt-sort-btn",
          style: {
            justifyContent: align === "right" ? "flex-end" : "flex-start"
          },
          onClick: () => onClick(column),
          "aria-label": label,
          title: label,
          children: [
            /* @__PURE__ */ jsx("span", { children: column }),
            indicator
          ]
        }
      )
    }
  );
}
const SortHeader = memo(SortHeaderInner);
function TableRowInner({
  data,
  row,
  diffMode,
  colorScheme,
  isLight = false,
  search,
  sandwichItem,
  onSymbolClick,
  onSearch,
  onSandwich
}) {
  const isSearched = search === `^${escapeRegex(row.symbol)}$`;
  const isSandwiched = sandwichItem === row.symbol;
  const selfDisp = data.valueDisplayProcessor(row.self);
  const totalDisp = data.valueDisplayProcessor(row.total);
  const profileTotal = data.getValue(0);
  const profileRight = data.getValueRight(0);
  const symbolColor = getSymbolColor(
    row.symbol,
    row.total,
    profileTotal + profileRight,
    row.totalRight,
    profileRight,
    isLight,
    colorScheme ?? "packageBased",
    diffMode
  );
  const onSandwichClick = useCallback(
    () => onSandwich(isSandwiched ? void 0 : row.symbol),
    [onSandwich, isSandwiched, row.symbol]
  );
  const onSearchClick = useCallback(
    () => onSearch(isSearched ? "" : row.symbol),
    [onSearch, isSearched, row.symbol]
  );
  const onLinkClick = useCallback(
    (e) => {
      e.preventDefault();
      onSymbolClick(row.symbol);
    },
    [onSymbolClick, row.symbol]
  );
  return /* @__PURE__ */ jsxs("tr", { role: "row", className: "fg-tt-row", style: { height: ROW_HEIGHT }, children: [
    /* @__PURE__ */ jsxs("td", { className: "fg-tt-action-cell", children: [
      /* @__PURE__ */ jsx(
        ActionButton,
        {
          icon: "sandwich",
          active: isSandwiched,
          label: isSandwiched ? "Remove from sandwich view" : "Show in sandwich view",
          onClick: onSandwichClick
        }
      ),
      /* @__PURE__ */ jsx(
        ActionButton,
        {
          icon: "search",
          active: isSearched,
          label: isSearched ? "Clear from search" : "Search for symbol",
          onClick: onSearchClick
        }
      )
    ] }),
    /* @__PURE__ */ jsx("td", { className: "fg-tt-symbol-cell", children: /* @__PURE__ */ jsx(
      "a",
      {
        href: "",
        role: "link",
        title: "Highlight symbol",
        "aria-label": row.symbol,
        className: "fg-tt-symbol-link",
        onClick: onLinkClick,
        children: row.symbol
      }
    ) }),
    data.isDiffFlamegraph() ? (() => {
      const leftTotal = data.getValue(0), rightTotal = data.getValueRight(0);
      const baseline = leftTotal ? (row.total - row.totalRight) / leftTotal * 100 : 0;
      const comparison = rightTotal ? row.totalRight / rightTotal * 100 : 0;
      const diff = baseline ? (comparison - baseline) / baseline * 100 : 0;
      if (diffMode === "absoluteDiff") {
        const left = row.total - row.totalRight, right = row.totalRight;
        const delta = right - left;
        return /* @__PURE__ */ jsxs(Fragment, { children: [
          /* @__PURE__ */ jsx(
            TableMetricCell,
            {
              label: "Baseline",
              value: left,
              total: leftTotal,
              display: formatValue(data.valueDisplayProcessor(left)),
              color: symbolColor
            }
          ),
          /* @__PURE__ */ jsx(
            TableMetricCell,
            {
              label: "Comparison",
              value: right,
              total: rightTotal,
              display: formatValue(data.valueDisplayProcessor(right)),
              color: symbolColor
            }
          ),
          /* @__PURE__ */ jsxs(
            "td",
            {
              className: "fg-tt-numeric-cell",
              style: {
                color: delta > 0 ? "var(--red-300)" : "var(--green-300)"
              },
              children: [
                delta >= 0 ? "+" : "\u2212",
                formatValue(data.valueDisplayProcessor(Math.abs(delta)))
              ]
            }
          )
        ] });
      }
      return /* @__PURE__ */ jsxs(Fragment, { children: [
        /* @__PURE__ */ jsx(
          TableMetricCell,
          {
            label: "Baseline",
            value: row.total - row.totalRight,
            total: leftTotal,
            display: `${baseline.toFixed(2)}%`,
            color: symbolColor
          }
        ),
        /* @__PURE__ */ jsx(
          TableMetricCell,
          {
            label: "Comparison",
            value: row.totalRight,
            total: rightTotal,
            display: `${comparison.toFixed(2)}%`,
            color: symbolColor
          }
        ),
        /* @__PURE__ */ jsx(
          "td",
          {
            className: "fg-tt-numeric-cell",
            style: {
              color: diff > 0 ? "var(--red-300)" : "var(--green-300)"
            },
            children: baseline ? `${diff.toFixed(2)}%` : "\u2014"
          }
        )
      ] });
    })() : /* @__PURE__ */ jsxs(Fragment, { children: [
      /* @__PURE__ */ jsx(
        TableMetricCell,
        {
          label: "Self",
          value: row.self,
          total: profileTotal,
          display: formatValue(selfDisp),
          color: symbolColor
        }
      ),
      /* @__PURE__ */ jsx(
        TableMetricCell,
        {
          label: "Total",
          value: row.total,
          total: profileTotal,
          display: formatValue(totalDisp),
          color: symbolColor
        }
      )
    ] })
  ] });
}
const TableRow = memo(TableRowInner);
function ActionButtonInner({
  icon,
  active,
  label,
  onClick
}) {
  return /* @__PURE__ */ jsx(
    "button",
    {
      type: "button",
      className: "fg-tt-action-btn",
      "data-active": active,
      onClick,
      "aria-label": label,
      title: label,
      children: /* @__PURE__ */ jsx(Icon, { name: icon, size: 14 })
    }
  );
}
const ActionButton = memo(ActionButtonInner);
function formatValue(disp) {
  return disp.text + (disp.suffix ?? "");
}
function buildFilteredTable(data, matchedLabels) {
  const filteredTable = /* @__PURE__ */ Object.create(null);
  const callStack = [];
  for (let i = 0; i < data.data.length; i++) {
    const value = data.getValue(i);
    const self = data.getSelf(i);
    const valueRight = data.getValueRight(i);
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
        filteredTable[label].totalRight = (filteredTable[label].totalRight ?? 0) + valueRight;
      }
    }
    callStack.push(label);
  }
  return filteredTable;
}

const ufuzzy = new uFuzzy();
const FlameGraphContainer = ({
  data,
  onTableSymbolClick,
  onViewSelected,
  onTextAlignSelected,
  onTableSort,
  stickyHeader,
  extraHeaderElements,
  vertical,
  showFlameGraphOnly,
  disableCollapsing,
  keepFocusOnDataChange,
  initialViewMode = "dual",
  getExtraContextMenuButtons
}) => {
  const [focusedItemData, setFocusedItemData] = useState();
  const [rangeMin, setRangeMin] = useState(0);
  const [rangeMax, setRangeMax] = useState(1);
  const [search, setSearch] = useState("");
  const [viewMode, setViewMode] = useState(initialViewMode);
  const [leftView, setLeftView] = useState("topTable");
  const [rightView, setRightView] = useState("flameGraph");
  const [singleView, setSingleView] = useState("flameGraph");
  const [singleDataMode, setSingleDataMode] = useState("proportionalDiff");
  const [leftDataMode, setLeftDataMode] = useState("baseline");
  const [rightDataMode, setRightDataMode] = useState("comparison");
  const [textAlign, setTextAlign] = useState("left");
  const [sandwichItem, setSandwichItem] = useState();
  const [collapsedMaps, setCollapsedMaps] = useState({});
  const onTableSymbolClickRef = useRef(onTableSymbolClick);
  const onTableSortRef = useRef(onTableSort);
  onTableSymbolClickRef.current = onTableSymbolClick;
  onTableSortRef.current = onTableSort;
  const isDiffData = Boolean(
    data?.fields.some((field) => field.name === "valueRight") && data?.fields.some((field) => field.name === "selfRight")
  );
  const containers = useMemo(() => {
    if (!data) return void 0;
    const make = (mode) => {
      const frame = !isDiffData || mode === "proportionalDiff" || mode === "absoluteDiff" ? data : {
        ...data,
        fields: data.fields.filter(
          (field) => field.name !== "valueRight" && field.name !== "selfRight"
        ).map((field) => {
          if (mode === "comparison" && (field.name === "value" || field.name === "self")) {
            const right = data.fields.find(
              (candidate) => candidate.name === `${field.name}Right`
            );
            return { ...right, name: field.name };
          }
          return field;
        })
      };
      return new FlameGraphDataContainer(frame, {
        collapsing: !disableCollapsing
      });
    };
    const diff = make("proportionalDiff");
    return {
      proportionalDiff: diff,
      absoluteDiff: diff,
      baseline: make("baseline"),
      comparison: make("comparison")
    };
  }, [data, isDiffData, disableCollapsing]);
  const dataContainer = containers?.proportionalDiff;
  useEffect(() => {
    if (containers)
      setCollapsedMaps({
        baseline: containers.baseline.getCollapsedMap(),
        comparison: containers.comparison.getCollapsedMap(),
        proportionalDiff: containers.proportionalDiff.getCollapsedMap(),
        absoluteDiff: containers.absoluteDiff.getCollapsedMap()
      });
  }, [containers]);
  const [colorScheme, setColorScheme] = useColorScheme(dataContainer);
  const [regularColorScheme, setRegularColorScheme] = useState(
    ColorScheme.PackageBased
  );
  const visibleModes = viewMode === "single" ? [singleDataMode] : [leftDataMode, rightDataMode];
  const showingDiff = isDiffData && visibleModes.some(
    (mode) => mode === "proportionalDiff" || mode === "absoluteDiff"
  );
  const matchedLabels = useLabelSearch(search, dataContainer);
  const resetFocus = useCallback(() => {
    setFocusedItemData(void 0);
    setRangeMin(0);
    setRangeMax(1);
  }, [setFocusedItemData, setRangeMax, setRangeMin]);
  const resetSandwich = useCallback(() => {
    setSandwichItem(void 0);
  }, [setSandwichItem]);
  useEffect(() => {
    if (!keepFocusOnDataChange) {
      resetFocus();
      resetSandwich();
      return;
    }
    if (dataContainer && focusedItemData) {
      const item = dataContainer.getNodesWithLabel(focusedItemData.label)?.[0];
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
      const anchored = `^${escapeRegex(symbol)}$`;
      if (search === anchored) {
        setSearch("");
      } else {
        onTableSymbolClickRef.current?.(symbol);
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
      setSearch(`^${escapeRegex(str)}$`);
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
    onTableSortRef.current?.(sort);
  }, []);
  if (!dataContainer) {
    return null;
  }
  const activeCollapsedMap = visibleModes.map((mode) => collapsedMaps[mode] ?? containers[mode].getCollapsedMap()).find((map) => map.size() > 0) ?? dataContainer.getCollapsedMap();
  const schemeFor = (mode) => containers[mode].isDiffFlamegraph() ? colorScheme : regularColorScheme;
  const flameGraph = (mode) => {
    const paneData = containers[mode];
    return /* @__PURE__ */ jsx(
      FlameGraph,
      {
        data: paneData,
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
        colorScheme: schemeFor(mode),
        diffMode: mode === "absoluteDiff" ? mode : "proportionalDiff",
        showFlameGraphOnly,
        collapsing: !disableCollapsing,
        getExtraContextMenuButtons,
        search,
        collapsedMap: (collapsedMaps[mode] ?? paneData.getCollapsedMap()).expandMatchingLabels(paneData, matchedLabels),
        setCollapsedMap: (map) => setCollapsedMaps((previous) => ({ ...previous, [mode]: map }))
      },
      mode
    );
  };
  const table = (mode) => /* @__PURE__ */ jsx(
    FlameGraphTopTableContainer,
    {
      data: containers[mode],
      diffMode: mode === "absoluteDiff" ? mode : "proportionalDiff",
      colorScheme: schemeFor(mode),
      onSymbolClick,
      search,
      matchedLabels,
      sandwichItem,
      onSandwich: setSandwichItem,
      onSearch,
      onTableSort: onTableSortStable
    }
  );
  const pane = (view, mode) => view === "flameGraph" ? flameGraph(mode) : view === "topTable" ? /* @__PURE__ */ jsx("div", { className: "fg-table-container", children: table(mode) }) : /* @__PURE__ */ jsx(
    StacktraceTable,
    {
      data: containers[mode],
      diffMode: mode === "absoluteDiff" ? mode : "proportionalDiff",
      colorScheme: schemeFor(mode),
      search,
      onSymbolClick
    }
  );
  const viewPicker = (view, position, select) => /* @__PURE__ */ jsxs("div", { className: "fg-view-picker", children: [
    /* @__PURE__ */ jsx("span", { children: "Visualization" }),
    /* @__PURE__ */ jsx(
      "div",
      {
        className: "fg-segmented",
        role: "group",
        "aria-label": `${position} pane view`,
        children: [
          ["flameGraph", "Flame graph", "Show the profile as a flame graph"],
          [
            "topTable",
            "Top table",
            "Show functions ranked by their sample values"
          ],
          ["callTree", "Call tree", "Show the profile as a stack trace tree"]
        ].map(([value, label, tooltip]) => /* @__PURE__ */ jsx(
          "button",
          {
            type: "button",
            title: tooltip,
            "aria-pressed": view === value,
            onClick: () => {
              select(value);
              onViewSelected?.(value);
            },
            children: label
          },
          value
        ))
      }
    )
  ] });
  const dataPicker = (mode, position, select) => isDiffData && /* @__PURE__ */ jsxs("div", { className: "fg-data-picker", children: [
    /* @__PURE__ */ jsx("span", { children: "Data" }),
    /* @__PURE__ */ jsx(
      "div",
      {
        className: "fg-segmented",
        role: "group",
        "aria-label": `${position} pane data`,
        children: [
          "baseline",
          "comparison",
          "proportionalDiff",
          "absoluteDiff"
        ].map((value) => /* @__PURE__ */ jsx(
          "button",
          {
            type: "button",
            title: {
              baseline: "Show the baseline profile",
              comparison: "Show the comparison profile",
              proportionalDiff: "Compare each function\u2019s share of its profile",
              absoluteDiff: "Compare raw sample counts (comparison minus baseline)"
            }[value],
            "aria-pressed": mode === value,
            onClick: () => {
              select(value);
              resetFocus();
            },
            children: {
              baseline: "Baseline",
              comparison: "Comparison",
              proportionalDiff: "Proportional diff",
              absoluteDiff: "Absolute diff"
            }[value]
          },
          value
        ))
      }
    )
  ] });
  return /* @__PURE__ */ jsxs("div", { className: "fg-container", children: [
    !showFlameGraphOnly && /* @__PURE__ */ jsx(
      FlameGraphHeader,
      {
        search,
        setSearch,
        viewMode,
        setViewMode,
        onReset: () => {
          resetFocus();
          resetSandwich();
        },
        textAlign,
        onTextAlignChange: (align) => {
          setTextAlign(align);
          onTextAlignSelected?.(align);
        },
        showResetButton: Boolean(focusedItemData || sandwichItem),
        colorScheme: showingDiff ? colorScheme : regularColorScheme,
        isDiffMode: showingDiff,
        onColorSchemeChange: (scheme) => {
          if (showingDiff) setColorScheme(scheme);
          else if (scheme === ColorScheme.PackageBased || scheme === ColorScheme.ValueBased)
            setRegularColorScheme(scheme);
        },
        stickyHeader: Boolean(stickyHeader),
        extraHeaderElements,
        vertical,
        setCollapsedMap: (map) => {
          const collapsed = Array.from(map.values()).every(
            (config) => config.collapsed
          );
          setCollapsedMaps((previous) => {
            const next = { ...previous };
            for (const mode of [
              "baseline",
              "comparison",
              "proportionalDiff",
              "absoluteDiff"
            ]) {
              const current = previous[mode] ?? containers[mode].getCollapsedMap();
              next[mode] = current.setAllCollapsedStatus(collapsed);
            }
            return next;
          });
        },
        collapsedMap: activeCollapsedMap,
        disableCollapsing
      }
    ),
    /* @__PURE__ */ jsx("div", { className: "fg-body", children: showFlameGraphOnly ? flameGraph(singleDataMode) : viewMode === "single" ? /* @__PURE__ */ jsxs("div", { className: "fg-pane-with-picker", children: [
      /* @__PURE__ */ jsxs("div", { className: "fg-pane-controls", children: [
        viewPicker(singleView, "View", setSingleView),
        dataPicker(singleDataMode, "View", setSingleDataMode)
      ] }),
      /* @__PURE__ */ jsx("div", { className: "fg-pane-content", children: pane(singleView, singleDataMode) })
    ] }) : /* @__PURE__ */ jsxs("div", { className: vertical ? "fg-dual-vertical" : "fg-dual", children: [
      /* @__PURE__ */ jsxs("div", { className: "fg-dual-pane", children: [
        /* @__PURE__ */ jsxs("div", { className: "fg-pane-controls", children: [
          viewPicker(leftView, vertical ? "Top" : "Left", setLeftView),
          dataPicker(
            leftDataMode,
            vertical ? "Top" : "Left",
            setLeftDataMode
          )
        ] }),
        /* @__PURE__ */ jsx("div", { className: "fg-pane-content", children: pane(leftView, leftDataMode) })
      ] }),
      /* @__PURE__ */ jsxs("div", { className: "fg-dual-pane", children: [
        /* @__PURE__ */ jsxs("div", { className: "fg-pane-controls", children: [
          viewPicker(
            rightView,
            vertical ? "Bottom" : "Right",
            setRightView
          ),
          dataPicker(
            rightDataMode,
            vertical ? "Bottom" : "Right",
            setRightDataMode
          )
        ] }),
        /* @__PURE__ */ jsx("div", { className: "fg-pane-content", children: pane(rightView, rightDataMode) })
      ] })
    ] }) })
  ] });
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
    for (const label of labels) {
      if (!regex.test(label)) {
        continue;
      }
      foundLabels.add(label);
      foundMatch = true;
    }
    return foundMatch;
  };
  const fuzzyFilter = (labels, term) => {
    const idxs = ufuzzy.filter(labels, term);
    if (!idxs) {
      return false;
    }
    let foundMatch = false;
    for (const idx of idxs) {
      foundLabels.add(labels[idx]);
      foundMatch = true;
    }
    return foundMatch;
  };
  for (const term of terms) {
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

export { FieldType, FlameGraphContainer as FlameGraph, checkFields, getMessageCheckFieldsResult };

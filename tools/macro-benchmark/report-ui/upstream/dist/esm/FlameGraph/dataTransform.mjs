import { FieldType, getDisplayProcessor, createTheme } from '@grafana/data';
import { SampleUnit } from '../types.mjs';
import { mergeParentSubtrees, mergeSubtrees } from './treeTransforms.mjs';

"use strict";
function nestedSetToLevels(container, options) {
  const levels = [];
  let offset = 0;
  let parent = void 0;
  const uniqueLabels = /* @__PURE__ */ Object.create(null);
  for (let i = 0; i < container.data.length; i++) {
    const currentLevel = container.getLevel(i);
    const prevLevel = i > 0 ? container.getLevel(i - 1) : void 0;
    levels[currentLevel] = levels[currentLevel] || [];
    if (prevLevel && prevLevel >= currentLevel) {
      const lastSibling = levels[currentLevel][levels[currentLevel].length - 1];
      offset = lastSibling.start + container.getValue(lastSibling.itemIndexes[0]) + container.getValueRight(lastSibling.itemIndexes[0]);
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
  const collapsedMapContainer = new CollapsedMapBuilder(options == null ? void 0 : options.collapsingThreshold);
  if (options == null ? void 0 : options.collapsing) {
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
    var _a;
    const stack = [root];
    while (stack.length) {
      const current = stack.shift();
      if ((_a = current.parents) == null ? void 0 : _a.length) {
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
    return `Data has fields of wrong type: ${wrongFields.wrongTypeFields.map((f) => `${f.name} has type ${f.type} but should be ${f.expectedTypes.join(" or ")}`).join(", ")}`;
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
    const frameField = data == null ? void 0 : data.fields.find((f) => f.name === name);
    if (!frameField) {
      missingFields.push(name);
      continue;
    }
    if (!types.includes(frameField.type)) {
      wrongTypeFields.push({ name, expectedTypes: types, type: frameField.type });
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
  constructor(data, options, theme = createTheme()) {
    var _a, _b, _c;
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
    if ((this.valueField || this.selfField) && !(this.valueField && this.selfField)) {
      throw new Error(
        "Malformed dataFrame: both valueRight and selfRight has to be present if one of them is present."
      );
    }
    const enumConfig = (_c = (_b = (_a = this.labelField) == null ? void 0 : _a.config) == null ? void 0 : _b.type) == null ? void 0 : _c.enum;
    if (enumConfig) {
      this.labelDisplayProcessor = getDisplayProcessor({ field: this.labelField, theme });
      this.uniqueLabels = enumConfig.text || [];
    } else {
      this.labelDisplayProcessor = (value) => ({
        text: value + "",
        numeric: 0
      });
      this.uniqueLabels = [...new Set(this.labelField.values)];
    }
    this.valueDisplayProcessor = getDisplayProcessor({
      field: this.valueField,
      theme
    });
  }
  isDiffFlamegraph() {
    return Boolean(this.valueRightField && this.selfRightField);
  }
  getLabel(index) {
    return this.labelDisplayProcessor(this.labelField.values[index]).text;
  }
  getLevel(index) {
    return this.levelField.values[index];
  }
  getValue(index) {
    return fieldAccessor(this.valueField, index);
  }
  getValueRight(index) {
    return fieldAccessor(this.valueRightField, index);
  }
  getSelf(index) {
    return fieldAccessor(this.selfField, index);
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
    if (!(nodes == null ? void 0 : nodes.length)) {
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
      const [levels, uniqueLabelsMap, collapsedMap] = nestedSetToLevels(this, this.options);
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
  let indexArray = typeof index === "number" ? [index] : index;
  return indexArray.reduce((acc, index2) => {
    return acc + field.values[index2];
  }, 0);
}

export { CollapsedMap, CollapsedMapBuilder, FlameGraphDataContainer, checkFields, getMessageCheckFieldsResult, nestedSetToLevels };
//# sourceMappingURL=dataTransform.mjs.map

import { getBarColorByDiff, getBarColorByValue, getBarColorByPackage } from '../FlameGraph/colors.mjs';
import { ColorScheme } from '../types.mjs';

"use strict";
function computeDiffPercent(totalPercent, totalPercentRight) {
  if (totalPercent > 0) {
    return (totalPercentRight - totalPercent) / totalPercent * 100;
  }
  if (totalPercentRight > 0) {
    return Infinity;
  }
  return 0;
}
function buildAllCallTreeNodes(data) {
  const levels = data.getLevels();
  if (levels.length === 0) {
    return [];
  }
  const rootItem = levels[0][0];
  let rootTotalLeft;
  let rootTotalRight;
  if (data.isDiffFlamegraph()) {
    rootTotalRight = rootItem.valueRight || 0;
    rootTotalLeft = rootItem.value - rootTotalRight;
  } else {
    rootTotalLeft = rootItem.value;
    rootTotalRight = 0;
  }
  return levels[0].map(
    (item, index) => buildCallTreeNode(data, item, rootTotalLeft, rootTotalRight, void 0, -1, index)
  );
}
function buildCallTreeNode(data, rootItem, rootTotalLeft, rootTotalRight, parentId, parentDepth = -1, childIndex = 0) {
  const nodeId = parentId ? `${parentId}.${childIndex}` : `${childIndex}`;
  const depth = parentDepth + 1;
  const label = data.getLabel(rootItem.itemIndexes[0]);
  let self;
  let total;
  let selfPercent;
  let totalPercent;
  let selfRight;
  let totalRight;
  let selfPercentRight;
  let totalPercentRight;
  let diffPercent;
  if (data.isDiffFlamegraph()) {
    const selfLeft = data.getSelf(rootItem.itemIndexes);
    selfRight = data.getSelfRight(rootItem.itemIndexes);
    const totalLeft = rootItem.value - (rootItem.valueRight || 0);
    totalRight = rootItem.valueRight || 0;
    self = selfLeft;
    total = totalLeft;
    selfPercent = rootTotalLeft > 0 ? selfLeft / rootTotalLeft * 100 : 0;
    totalPercent = rootTotalLeft > 0 ? totalLeft / rootTotalLeft * 100 : 0;
    selfPercentRight = rootTotalRight > 0 ? selfRight / rootTotalRight * 100 : 0;
    totalPercentRight = rootTotalRight > 0 ? totalRight / rootTotalRight * 100 : 0;
    diffPercent = computeDiffPercent(totalPercent, totalPercentRight);
  } else {
    self = data.getSelf(rootItem.itemIndexes);
    total = rootItem.value;
    const rootTotal = rootTotalLeft;
    selfPercent = rootTotal > 0 ? self / rootTotal * 100 : 0;
    totalPercent = rootTotal > 0 ? total / rootTotal * 100 : 0;
  }
  const children = rootItem.children.length > 0 ? rootItem.children.map((child, index) => {
    return buildCallTreeNode(data, child, rootTotalLeft, rootTotalRight, nodeId, depth, index);
  }) : void 0;
  const subtreeSize = children ? children.reduce((sum, child) => sum + child.subtreeSize + 1, 0) : 0;
  return {
    id: nodeId,
    label,
    self,
    total,
    selfPercent,
    totalPercent,
    depth,
    parentId,
    subtreeSize,
    levelItem: rootItem,
    children,
    selfRight,
    totalRight,
    selfPercentRight,
    totalPercentRight,
    diffPercent
  };
}
function getInitialExpandedState(nodes, levelsToExpand = 2) {
  const expanded = {};
  nodes.forEach((node) => {
    collectExpandedByDepth(node, levelsToExpand, expanded);
  });
  return expanded;
}
function collectExpandedByDepth(node, levelsToExpand, expanded) {
  if (node.depth < levelsToExpand && node.children && node.children.length > 0) {
    expanded[node.id] = true;
  }
  if (node.children) {
    node.children.forEach((child) => collectExpandedByDepth(child, levelsToExpand, expanded));
  }
}
function buildCallersTree(levels, data) {
  if (levels.length === 0) {
    return [];
  }
  const targetLevel = levels[levels.length - 1];
  if (!targetLevel || targetLevel.length === 0) {
    return [];
  }
  let targetTotalLeft;
  let targetTotalRight;
  if (data.isDiffFlamegraph()) {
    targetTotalRight = targetLevel.reduce((sum, item) => sum + (item.valueRight || 0), 0);
    targetTotalLeft = targetLevel.reduce((sum, item) => sum + item.value, 0) - targetTotalRight;
  } else {
    targetTotalLeft = targetLevel.reduce((sum, item) => sum + item.value, 0);
    targetTotalRight = 0;
  }
  const buildNode = (item, nodeId, depth, parentId) => {
    const label = data.getLabel(item.itemIndexes[0]);
    let self;
    let total;
    let selfPercent;
    let totalPercent;
    let selfRight;
    let totalRight;
    let selfPercentRight;
    let totalPercentRight;
    let diffPercent;
    if (data.isDiffFlamegraph()) {
      const selfLeft = data.getSelf(item.itemIndexes);
      selfRight = data.getSelfRight(item.itemIndexes);
      const totalLeft = item.value - (item.valueRight || 0);
      totalRight = item.valueRight || 0;
      self = selfLeft;
      total = totalLeft;
      selfPercent = targetTotalLeft > 0 ? selfLeft / targetTotalLeft * 100 : 0;
      totalPercent = targetTotalLeft > 0 ? totalLeft / targetTotalLeft * 100 : 0;
      selfPercentRight = targetTotalRight > 0 ? selfRight / targetTotalRight * 100 : 0;
      totalPercentRight = targetTotalRight > 0 ? totalRight / targetTotalRight * 100 : 0;
      diffPercent = computeDiffPercent(totalPercent, totalPercentRight);
    } else {
      self = data.getSelf(item.itemIndexes);
      total = item.value;
      selfPercent = targetTotalLeft > 0 ? self / targetTotalLeft * 100 : 0;
      totalPercent = targetTotalLeft > 0 ? total / targetTotalLeft * 100 : 0;
    }
    const callers = item.parents || [];
    const children = callers.length > 0 ? callers.map((caller, idx) => {
      return buildNode(caller, `${nodeId}.${idx}`, depth + 1, nodeId);
    }) : void 0;
    const subtreeSize = children ? children.reduce((sum, child) => sum + child.subtreeSize + 1, 0) : 0;
    return {
      id: nodeId,
      label,
      self,
      total,
      selfPercent,
      totalPercent,
      depth,
      parentId,
      subtreeSize,
      levelItem: item,
      children,
      selfRight,
      totalRight,
      selfPercentRight,
      totalPercentRight,
      diffPercent
    };
  };
  return targetLevel.map((targetItem, index) => buildNode(targetItem, `${index}`, 0, void 0));
}
function getRowBarColor(node, data, colorScheme, theme) {
  if (data.isDiffFlamegraph()) {
    const levels = data.getLevels();
    const rootTotal = levels[0][0].value;
    const rootTotalRight = levels[0][0].valueRight || 0;
    const barColor = getBarColorByDiff(
      node.total + (node.totalRight || 0),
      node.totalRight || 0,
      rootTotal,
      rootTotalRight,
      // eslint-disable-next-line @typescript-eslint/consistent-type-assertions
      colorScheme
    );
    return barColor.setAlpha(1).toString();
  } else {
    if (colorScheme === ColorScheme.ValueBased) {
      const levels = data.getLevels();
      const rootTotal = levels[0][0].value;
      const barColor = getBarColorByValue(node.total, rootTotal, 0, 1);
      return barColor.setAlpha(1).toString();
    } else {
      const barColor = getBarColorByPackage(node.label, theme);
      return barColor.setAlpha(1).toString();
    }
  }
}

export { buildAllCallTreeNodes, buildCallersTree, getInitialExpandedState, getRowBarColor };
//# sourceMappingURL=utils.mjs.map

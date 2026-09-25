import { jsx, jsxs } from 'react/jsx-runtime';
import { memo, useMemo } from 'react';
import { Menu, Dropdown, IconButton } from '@grafana/ui';
import { PaneView, ViewMode } from '../types.mjs';

"use strict";
const ActionsCell = memo(function ActionsCell2({
  nodeId,
  label,
  itemIndexes,
  levelItem,
  hasChildren,
  depth,
  parentId,
  onFocus,
  onShowCallers,
  onSearch,
  focusedNodeId,
  callersNodeLabel,
  isSearchMatch,
  actionsCellClass,
  getExtraContextMenuButtons,
  data,
  viewMode,
  paneView,
  search
}) {
  const isTheFocusedNode = nodeId === focusedNodeId || (focusedNodeId == null ? void 0 : focusedNodeId.startsWith("label:")) && focusedNodeId.substring(6) === label;
  const isTheCallersTarget = label === callersNodeLabel;
  const inCallersMode = callersNodeLabel !== void 0;
  const inFocusMode = focusedNodeId !== void 0;
  const isRootNode = depth === 0 && !parentId;
  const shouldShowFocusItem = hasChildren && !isTheFocusedNode && !(isRootNode && !inFocusMode);
  const shouldShowCallersItem = !isTheCallersTarget && !isRootNode;
  const shouldShowSearchItem = onSearch && !isSearchMatch;
  const extraButtons = useMemo(() => {
    if (!getExtraContextMenuButtons) {
      return [];
    }
    const clickedItemData = {
      label,
      item: levelItem,
      posX: 0,
      posY: 0
    };
    return getExtraContextMenuButtons(clickedItemData, data.data, {
      viewMode: viewMode != null ? viewMode : ViewMode.Single,
      paneView: paneView != null ? paneView : PaneView.CallTree,
      isDiff: data.isDiffFlamegraph(),
      search
    });
  }, [getExtraContextMenuButtons, label, levelItem, data, viewMode, paneView, search]);
  const hasAnyAction = shouldShowFocusItem || shouldShowCallersItem || shouldShowSearchItem || extraButtons.length > 0;
  if (!hasAnyAction) {
    return /* @__PURE__ */ jsx("div", { className: actionsCellClass });
  }
  const menu = /* @__PURE__ */ jsxs(Menu, { children: [
    shouldShowFocusItem && /* @__PURE__ */ jsx(
      Menu.Item,
      {
        label: "Focus on callees",
        icon: "compress-arrows",
        onClick: () => {
          if (inCallersMode) {
            onFocus(label, true, itemIndexes);
          } else {
            onFocus(nodeId, false, itemIndexes);
          }
        }
      }
    ),
    shouldShowCallersItem && /* @__PURE__ */ jsx(Menu.Item, { label: "Show callers", icon: "expand-arrows-alt", onClick: () => onShowCallers(label) }),
    shouldShowSearchItem && /* @__PURE__ */ jsx(Menu.Item, { label: "Search", icon: "search", onClick: () => onSearch(label) }),
    extraButtons.map(({ label: btnLabel, icon, onClick }) => /* @__PURE__ */ jsx(Menu.Item, { label: btnLabel, icon, onClick }, btnLabel))
  ] });
  return /* @__PURE__ */ jsx("div", { className: actionsCellClass, children: /* @__PURE__ */ jsx(Dropdown, { overlay: menu, children: /* @__PURE__ */ jsx(IconButton, { name: "ellipsis-v", "aria-label": "Actions", size: "sm", onClick: (e) => e.stopPropagation() }) }) });
});

export { ActionsCell };
//# sourceMappingURL=ActionsCell.mjs.map

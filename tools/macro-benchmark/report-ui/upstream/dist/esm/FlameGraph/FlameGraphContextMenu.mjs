import { jsxs, Fragment, jsx } from 'react/jsx-runtime';
import { MenuItem, MenuGroup, ContextMenu } from '@grafana/ui';

"use strict";
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
  viewMode,
  paneView,
  search
}) => {
  function renderItems() {
    const extraButtons = (getExtraContextMenuButtons == null ? void 0 : getExtraContextMenuButtons(itemData, data.data, {
      selectedView,
      viewMode,
      paneView,
      isDiff: data.isDiffFlamegraph(),
      search,
      collapseConfig
    })) || [];
    return /* @__PURE__ */ jsxs(Fragment, { children: [
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
          icon: "gf-show-context",
          onClick: () => {
            onSandwich();
            onMenuItemClick();
          }
        }
      ),
      extraButtons.map(({ label, icon, onClick }) => {
        return /* @__PURE__ */ jsx(MenuItem, { label, icon, onClick: () => onClick() }, label);
      }),
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
    ] });
  }
  return /* @__PURE__ */ jsx("div", { "data-testid": "contextMenu", children: /* @__PURE__ */ jsx(
    ContextMenu,
    {
      renderMenuItems: renderItems,
      x: itemData.posX + 10,
      y: itemData.posY,
      focusOnOpen: false
    }
  ) });
};

export { FlameGraphContextMenu as default };
//# sourceMappingURL=FlameGraphContextMenu.mjs.map

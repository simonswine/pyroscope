import { type DataFrame } from '@grafana/data';
import { type IconName } from '@grafana/ui';
import { type ClickedItemData, type PaneView, type SelectedView, type ViewMode } from '../types';
import { type CollapseConfig, type FlameGraphDataContainer } from './dataTransform';
export type GetExtraContextMenuButtonsFunction = (clickedItemData: ClickedItemData, data: DataFrame, state: {
    selectedView?: SelectedView;
    viewMode?: ViewMode;
    paneView?: PaneView;
    isDiff: boolean;
    search: string;
    collapseConfig?: CollapseConfig;
}) => ExtraContextMenuButton[];
type ExtraContextMenuButton = {
    label: string;
    icon: IconName;
    onClick: () => void;
};
type Props = {
    data: FlameGraphDataContainer;
    itemData: ClickedItemData;
    onMenuItemClick: () => void;
    onItemFocus: () => void;
    onSandwich: () => void;
    onExpandGroup: () => void;
    onCollapseGroup: () => void;
    onExpandAllGroups: () => void;
    onCollapseAllGroups: () => void;
    getExtraContextMenuButtons?: GetExtraContextMenuButtonsFunction;
    collapseConfig?: CollapseConfig;
    collapsing?: boolean;
    allGroupsCollapsed?: boolean;
    allGroupsExpanded?: boolean;
    selectedView?: SelectedView;
    viewMode?: ViewMode;
    paneView?: PaneView;
    search: string;
};
declare const FlameGraphContextMenu: ({ data, itemData, onMenuItemClick, onItemFocus, onSandwich, collapseConfig, onExpandGroup, onCollapseGroup, onExpandAllGroups, onCollapseAllGroups, getExtraContextMenuButtons, collapsing, allGroupsExpanded, allGroupsCollapsed, selectedView, viewMode, paneView, search, }: Props) => import("react").JSX.Element;
export default FlameGraphContextMenu;

import * as React from 'react';
import { type DataFrame, type GrafanaTheme2 } from '@grafana/data';
import { type GetExtraContextMenuButtonsFunction } from './FlameGraph/FlameGraphContextMenu';
import { FlameGraphDataContainer } from './FlameGraph/dataTransform';
export type Props = {
    /**
     * DataFrame with the profile data. The dataFrame needs to have the following fields:
     * label: string - the label of the node
     * level: number - the nesting level of the node
     * value: number - the total value of the node
     * self: number - the self value of the node
     * Optionally if it represents diff of 2 different profiles it can also have fields:
     * valueRight: number - the total value of the node in the right profile
     * selfRight: number - the self value of the node in the right profile
     */
    data?: DataFrame;
    /**
     * Whether the header should be sticky and be always visible on the top when scrolling.
     */
    stickyHeader?: boolean;
    /**
     * Provides a theme for the visualization on which colors and some sizes are based.
     */
    getTheme: () => GrafanaTheme2;
    /**
     * Various interaction hooks that can be used to report on the interaction.
     */
    onTableSymbolClick?: (symbol: string) => void;
    onViewSelected?: (view: string) => void;
    onTextAlignSelected?: (align: string) => void;
    onTableSort?: (sort: string) => void;
    /**
     * Elements that will be shown in the header on the right side of the header buttons. Useful for additional
     * functionality.
     */
    extraHeaderElements?: React.ReactNode;
    /**
     * Extra buttons that will be shown in the context menu when user clicks on a Node.
     */
    getExtraContextMenuButtons?: GetExtraContextMenuButtonsFunction;
    /**
     * If true the flamegraph will be rendered on top of the table.
     */
    vertical?: boolean;
    /**
     * If true only the flamegraph will be rendered.
     */
    showFlameGraphOnly?: boolean;
    /**
     * Disable behaviour where similar items in the same stack will be collapsed into single item.
     */
    disableCollapsing?: boolean;
    /**
     * Whether or not to keep any focused item when the profile data changes.
     */
    keepFocusOnDataChange?: boolean;
    /**
     * If true, the assistant button will be shown in the header if available.
     * This is needed mainly for Profiles Drilldown where in some cases we need to hide the button to show alternative
     * option to use AI.
     * @default true
     */
    showAnalyzeWithAssistant?: boolean;
    /**
     * Enable the new pane-based UI with call tree support.
     */
    enableNewUI?: boolean;
};
declare const FlameGraphContainer: ({ data, onTableSymbolClick, onViewSelected, onTextAlignSelected, onTableSort, getTheme, stickyHeader, extraHeaderElements, vertical, showFlameGraphOnly, disableCollapsing, keepFocusOnDataChange, getExtraContextMenuButtons, showAnalyzeWithAssistant, enableNewUI, }: Props) => React.JSX.Element;
export declare function labelSearch(search: string, data: FlameGraphDataContainer): Set<string>;
export default FlameGraphContainer;

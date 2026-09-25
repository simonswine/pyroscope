import { type GetExtraContextMenuButtonsFunction } from '../FlameGraph/FlameGraphContextMenu';
import { type FlameGraphDataContainer } from '../FlameGraph/dataTransform';
import { type PaneView, type ViewMode } from '../types';
type Props = {
    data: FlameGraphDataContainer;
    onSymbolClick: (symbol: string) => void;
    sandwichItem?: string;
    onSandwich: (str?: string) => void;
    onTableSort?: (sort: string) => void;
    search: string;
    onSearch?: (symbol: string) => void;
    focusedItemIndexes?: number[];
    setFocusedItemIndexes?: (itemIndexes: number[] | undefined) => void;
    getExtraContextMenuButtons?: GetExtraContextMenuButtonsFunction;
    viewMode?: ViewMode;
    paneView?: PaneView;
};
declare const FlameGraphCallTreeContainer: import("react").MemoExoticComponent<({ data, onSymbolClick, sandwichItem, onSandwich, search, onSearch, focusedItemIndexes, setFocusedItemIndexes, getExtraContextMenuButtons, viewMode, paneView, }: Props) => import("react").JSX.Element>;
export default FlameGraphCallTreeContainer;

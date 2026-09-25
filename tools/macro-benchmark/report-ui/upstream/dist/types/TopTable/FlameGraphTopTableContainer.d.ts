import { type FlameGraphDataContainer } from '../FlameGraph/dataTransform';
import { type ColorScheme, ColorSchemeDiff, type TableData } from '../types';
type Props = {
    data: FlameGraphDataContainer;
    onSymbolClick: (symbol: string) => void;
    search?: string;
    matchedLabels?: Set<string>;
    sandwichItem?: string;
    onSearch: (str: string) => void;
    onSandwich: (str?: string) => void;
    onTableSort?: (sort: string) => void;
    colorScheme: ColorScheme | ColorSchemeDiff;
};
declare const FlameGraphTopTableContainer: import("react").MemoExoticComponent<({ data, onSymbolClick, search, matchedLabels, onSearch, sandwichItem, onSandwich, onTableSort, colorScheme, }: Props) => import("react").JSX.Element>;
declare function buildFilteredTable(data: FlameGraphDataContainer, matchedLabels?: Set<string>): {
    [key: string]: TableData;
};
export { buildFilteredTable };
export default FlameGraphTopTableContainer;

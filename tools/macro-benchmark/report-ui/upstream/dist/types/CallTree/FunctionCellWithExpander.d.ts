import { type Row, type UseExpandedRowProps } from 'react-table';
import { type CallTreeNode } from './utils';
export declare function FunctionCellWithExpander({ row, value, depth, hasChildren, rowIndex, rows, onSymbolClick, compact, toggleRowExpanded, }: {
    row: Row<CallTreeNode> & UseExpandedRowProps<CallTreeNode>;
    value: string;
    depth: number;
    hasChildren: boolean;
    rowIndex?: number;
    rows: Array<Row<CallTreeNode>>;
    onSymbolClick: (symbol: string) => void;
    compact?: boolean;
    toggleRowExpanded: (id: string[], value?: boolean) => void;
}): import("react").JSX.Element;

import { type Row, type HeaderGroup, type TablePropGetter, type TableBodyPropGetter, type TableProps, type TableBodyProps } from 'react-table';
import { type CallTreeNode } from './utils';
type CallTreeTableProps = {
    width: number;
    height: number;
    compactModeThreshold: number;
    isCompact: boolean;
    setIsCompact: (compact: boolean) => void;
    getFunctionColumnWidth: (availableWidth: number, compactMode: boolean) => number | undefined;
    getTableProps: (propGetter?: TablePropGetter<CallTreeNode>) => TableProps;
    getTableBodyProps: (propGetter?: TableBodyPropGetter<CallTreeNode>) => TableBodyProps;
    headerGroups: Array<HeaderGroup<CallTreeNode>>;
    rows: Array<Row<CallTreeNode>>;
    prepareRow: (row: Row<CallTreeNode>) => void;
    currentSearchMatchId?: string;
    searchMatchRowRef: (node: HTMLTableRowElement | null) => void;
    scrollContainerRef: {
        current: HTMLDivElement | null;
    };
    focusedNodeId?: string;
    callersNodeLabel?: string;
};
export declare function CallTreeTable({ width, height, compactModeThreshold, isCompact, setIsCompact, getFunctionColumnWidth, getTableProps, getTableBodyProps, headerGroups, rows, prepareRow, currentSearchMatchId, searchMatchRowRef, scrollContainerRef, focusedNodeId, callersNodeLabel, }: CallTreeTableProps): import("react").JSX.Element | null;
export {};

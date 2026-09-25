import { type LevelItem } from './FlameGraph/dataTransform';
export type ClickedItemData = {
    posX: number;
    posY: number;
    label: string;
    item: LevelItem;
};
export declare enum SampleUnit {
    Bytes = "bytes",
    Short = "short",
    Nanoseconds = "ns"
}
export declare enum SelectedView {
    TopTable = "topTable",
    FlameGraph = "flameGraph",
    Both = "both"
}
export declare enum ViewMode {
    Single = "single",
    Split = "split"
}
export declare enum PaneView {
    TopTable = "topTable",
    FlameGraph = "flameGraph",
    CallTree = "callTree"
}
export interface TableData {
    self: number;
    total: number;
    totalRight: number;
}
export declare enum ColorScheme {
    ValueBased = "valueBased",
    PackageBased = "packageBased"
}
export declare enum ColorSchemeDiff {
    Default = "default",
    DiffColorBlind = "diffColorBlind"
}
export type TextAlign = 'left' | 'right';

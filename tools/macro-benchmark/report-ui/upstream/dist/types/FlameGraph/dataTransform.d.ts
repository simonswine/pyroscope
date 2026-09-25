import { type DataFrame, type DisplayProcessor, type Field, FieldType, type GrafanaTheme2 } from '@grafana/data';
export type LevelItem = {
    start: number;
    value: number;
    valueRight?: number;
    itemIndexes: number[];
    children: LevelItem[];
    level: number;
    parents?: LevelItem[];
};
export type CollapseConfig = {
    items: LevelItem[];
    collapsed: boolean;
};
/**
 * Convert data frame with nested set format into array of level. This is mainly done for compatibility with current
 * rendering code.
 */
export declare function nestedSetToLevels(container: FlameGraphDataContainer, options?: Options): [LevelItem[][], Record<string, LevelItem[]>, CollapsedMap];
/**
 * Small wrapper around the map of items that should be visually collapsed in the flame graph. Reason this is a wrapper
 * is that we want to make sure that when this is in the state we don't update the map directly but create a new map
 * and to have a place for the methods to collapse/expand either single item or all the items.
 */
export declare class CollapsedMap {
    private map;
    constructor(map?: Map<LevelItem, CollapseConfig>);
    get(item: LevelItem): CollapseConfig | undefined;
    keys(): MapIterator<LevelItem>;
    values(): MapIterator<CollapseConfig>;
    size(): number;
    setCollapsedStatus(item: LevelItem, collapsed: boolean): CollapsedMap;
    setAllCollapsedStatus(collapsed: boolean): CollapsedMap;
}
/**
 * Similar to CollapsedMap but this one is mutable and used during transformation of the dataFrame data into structure
 * we use for rendering. This should not be passed to the React components.
 */
export declare class CollapsedMapBuilder {
    private map;
    private threshold;
    constructor(threshold?: number);
    addTree(root: LevelItem): void;
    addItem(item: LevelItem, parent?: LevelItem): void;
    getCollapsedMap(): CollapsedMap;
}
export declare function getMessageCheckFieldsResult(wrongFields: CheckFieldsResult): string;
type CheckFieldsResult = {
    wrongTypeFields: Array<{
        name: string;
        expectedTypes: FieldType[];
        type: FieldType;
    }>;
    missingFields: string[];
};
export declare function checkFields(data: DataFrame): CheckFieldsResult | undefined;
export type Options = {
    collapsing: boolean;
    collapsingThreshold?: number;
};
export declare class FlameGraphDataContainer {
    data: DataFrame;
    options: Options;
    labelField: Field;
    levelField: Field;
    valueField: Field;
    selfField: Field;
    valueRightField?: Field;
    selfRightField?: Field;
    labelDisplayProcessor: DisplayProcessor;
    valueDisplayProcessor: DisplayProcessor;
    uniqueLabels: string[];
    private levels;
    private uniqueLabelsMap;
    private collapsedMap;
    constructor(data: DataFrame, options: Options, theme?: GrafanaTheme2);
    isDiffFlamegraph(): boolean;
    getLabel(index: number): string;
    getLevel(index: number): any;
    getValue(index: number | number[]): number;
    getValueRight(index: number | number[]): number;
    getSelf(index: number | number[]): number;
    getSelfRight(index: number | number[]): number;
    getSelfDisplay(index: number | number[]): import("@grafana/data").DisplayValue;
    getUniqueLabels(): string[];
    getUnitTitle(): "Count" | "RAM" | "Time";
    getLevels(): LevelItem[][];
    getSandwichLevels(label: string): [LevelItem[][], LevelItem[][]];
    getNodesWithLabel(label: string): LevelItem[];
    getCollapsedMap(): CollapsedMap;
    private initLevels;
}
export {};

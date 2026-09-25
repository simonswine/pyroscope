import { type CollapseConfig, type FlameGraphDataContainer, type LevelItem } from './dataTransform';
type Props = {
    data: FlameGraphDataContainer;
    totalTicks: number;
    position?: {
        x: number;
        y: number;
    };
    item?: LevelItem;
    collapseConfig?: CollapseConfig;
};
declare const FlameGraphTooltip: ({ data, item, totalTicks, position, collapseConfig }: Props) => import("react").JSX.Element | null;
type TooltipData = {
    percentValue: number;
    percentSelf: number;
    unitTitle: string;
    unitValue: string;
    unitSelf: string;
    samples: string;
};
export declare const getTooltipData: (data: FlameGraphDataContainer, item: LevelItem, totalTicks: number) => TooltipData;
type DiffTableData = {
    rowId: string;
    label: string;
    baseline: string | number;
    comparison: string | number;
    diff: string | number;
};
export declare const getDiffTooltipData: (data: FlameGraphDataContainer, item: LevelItem, totalTicks: number) => DiffTableData[];
export default FlameGraphTooltip;

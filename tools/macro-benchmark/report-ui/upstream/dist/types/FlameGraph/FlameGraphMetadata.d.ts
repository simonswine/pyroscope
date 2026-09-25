import { type ClickedItemData } from '../types';
import { type FlameGraphDataContainer } from './dataTransform';
type Props = {
    data: FlameGraphDataContainer;
    totalTicks: number;
    onFocusPillClick: () => void;
    onSandwichPillClick: () => void;
    focusedItem?: ClickedItemData;
    sandwichedLabel?: string;
};
declare const FlameGraphMetadata: import("react").MemoExoticComponent<({ data, focusedItem, totalTicks, sandwichedLabel, onFocusPillClick, onSandwichPillClick }: Props) => import("react").JSX.Element>;
export default FlameGraphMetadata;

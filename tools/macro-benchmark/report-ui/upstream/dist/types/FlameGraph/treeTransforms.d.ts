import { type LevelItem } from './dataTransform';
type DataInterface = {
    getLabel: (index: number) => string;
};
export declare function mergeParentSubtrees(roots: LevelItem[], data: DataInterface): LevelItem[][];
export declare function mergeSubtrees(roots: LevelItem[], data: DataInterface, direction?: 'parents' | 'children'): LevelItem[][];
export {};

import { FlameGraphDataContainer, type LevelItem, type Options } from './dataTransform';
export declare function textToDataContainer(text: string, options?: Options): FlameGraphDataContainer | undefined;
export declare function trimLevelsString(s: string): string;
export declare function levelsToString(levels: LevelItem[][], data: FlameGraphDataContainer): string;

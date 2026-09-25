import { type FlameGraphDataContainer } from './FlameGraph/dataTransform';
import { ColorScheme, ColorSchemeDiff } from './types';
/**
 * Manages the color scheme state, resetting it when the data changes between
 * diff and non-diff profiles.
 */
export declare function useColorScheme(dataContainer: FlameGraphDataContainer | undefined): readonly [ColorScheme | ColorSchemeDiff, import("react").Dispatch<import("react").SetStateAction<ColorScheme | ColorSchemeDiff>>];

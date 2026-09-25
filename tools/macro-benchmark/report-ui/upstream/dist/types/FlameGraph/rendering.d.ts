import { type RefObject } from 'react';
import { type ClickedItemData, ColorScheme, ColorSchemeDiff, type TextAlign } from '../types';
import { type CollapsedMap, type FlameGraphDataContainer, type LevelItem } from './dataTransform';
type RenderOptions = {
    canvasRef: RefObject<HTMLCanvasElement | null>;
    data: FlameGraphDataContainer;
    root: LevelItem;
    direction: 'children' | 'parents';
    depth: number;
    wrapperWidth: number;
    rangeMin: number;
    rangeMax: number;
    matchedLabels: Set<string> | undefined;
    textAlign: TextAlign;
    totalViewTicks: number;
    totalColorTicks: number;
    totalTicksRight: number | undefined;
    colorScheme: ColorScheme | ColorSchemeDiff;
    focusedItemData?: ClickedItemData;
    collapsedMap: CollapsedMap;
};
export declare function useFlameRender(options: RenderOptions): void;
type RenderFuncWrap = (item: LevelItem, x: number, y: number, width: number, height: number, label: string, muted: boolean) => void;
/**
 * Exported for testing don't use directly
 * Walks the tree and computes coordinates, dimensions and other data needed for rendering. For each item in the tree
 * it defers the rendering to the renderFunc.
 */
export declare function walkTree(root: LevelItem, direction: 'children' | 'parents', data: FlameGraphDataContainer, totalViewTicks: number, rangeMin: number, rangeMax: number, wrapperWidth: number, collapsedMap: CollapsedMap, renderFunc: RenderFuncWrap, devicePixelRatio?: number): void;
/**
 * Returns the X position of the bar. totalTicks * rangeMin is to adjust for any current zoom. So if we zoom to a
 * section of the graph we align and shift the X coordinates accordingly.
 * @param offset
 * @param totalTicks
 * @param rangeMin
 * @param pixelsPerTick
 */
export declare function getBarX(offset: number, totalTicks: number, rangeMin: number, pixelsPerTick: number): number;
export {};

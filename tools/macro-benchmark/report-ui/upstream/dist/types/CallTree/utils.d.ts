import { type GrafanaTheme2 } from '@grafana/data';
import { type FlameGraphDataContainer, type LevelItem } from '../FlameGraph/dataTransform';
import { ColorScheme, type ColorSchemeDiff } from '../types';
export interface CallTreeNode {
    id: string;
    label: string;
    self: number;
    total: number;
    selfPercent: number;
    totalPercent: number;
    depth: number;
    parentId?: string;
    subtreeSize: number;
    levelItem: LevelItem;
    children?: CallTreeNode[];
    selfRight?: number;
    totalRight?: number;
    selfPercentRight?: number;
    totalPercentRight?: number;
    diffPercent?: number;
}
/**
 * Build all call tree nodes from the root level items.
 * Returns an array of root nodes, each with their children.
 * This handles cases where there might be multiple root items.
 *
 * For diff flame graphs, separates left (baseline) and right (comparison) totals
 * to match the Flame Graph's calculation method.
 */
export declare function buildAllCallTreeNodes(data: FlameGraphDataContainer): CallTreeNode[];
/**
 * Get initial expanded state for the tree.
 * Auto-expands first N levels.
 */
export declare function getInitialExpandedState(nodes: CallTreeNode[], levelsToExpand?: number): Record<string, boolean>;
/**
 * Build a callers tree from sandwich levels data.
 *
 * This follows the same pattern as the flame graph's sandwich mode:
 * - The sandwich transformation (mergeParentSubtrees) creates levels where:
 *   - The last level contains the target function(s)
 *   - Earlier levels contain callers, with `parents` pointing toward the target
 * - We start from the target (last level) and traverse via `parents` to build an inverted tree
 * - Values come directly from item.value (already transformed by sandwich transformation)
 * - Percentages are relative to the target's total (target = 100%), matching sandwich view
 */
export declare function buildCallersTree(levels: LevelItem[][], data: FlameGraphDataContainer): CallTreeNode[];
export declare function getRowBarColor(node: CallTreeNode, data: FlameGraphDataContainer, colorScheme: ColorScheme | ColorSchemeDiff, theme: GrafanaTheme2): string;

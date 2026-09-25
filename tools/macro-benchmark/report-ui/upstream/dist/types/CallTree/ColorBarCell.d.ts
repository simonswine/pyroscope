import { type GrafanaTheme2 } from '@grafana/data';
import { type FlameGraphDataContainer } from '../FlameGraph/dataTransform';
import { type ColorScheme, type ColorSchemeDiff } from '../types';
import { type CallTreeNode } from './utils';
export declare function ColorBarCell({ node, data, colorScheme, theme, focusedNode, }: {
    node: CallTreeNode;
    data: FlameGraphDataContainer;
    colorScheme: ColorScheme | ColorSchemeDiff;
    theme: GrafanaTheme2;
    focusedNode?: CallTreeNode;
}): import("react").JSX.Element;

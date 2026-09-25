import { ColorScheme, ColorSchemeDiff } from './types';
type ColorSchemeButtonProps = {
    value: ColorScheme | ColorSchemeDiff;
    onChange: (colorScheme: ColorScheme | ColorSchemeDiff) => void;
    isDiffMode: boolean;
};
export declare function ColorSchemeButton(props: ColorSchemeButtonProps): import("react").JSX.Element;
export {};

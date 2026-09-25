import { jsx, jsxs, Fragment } from 'react/jsx-runtime';
import { cx, css } from '@emotion/css';
import { useState, useEffect } from 'react';
import { usePrevious, useDebounce } from 'react-use';
import { OpenAssistantButton } from '@grafana/assistant';
import { useStyles2, Button, Input, RadioButtonGroup, IconButton, Dropdown, Menu, ButtonGroup } from '@grafana/ui';
import { ColorSchemeButton } from './ColorSchemeButton.mjs';
import { MIN_WIDTH_TO_SHOW_SPLIT_PANE_SELECTORS, MIN_WIDTH_TO_SHOW_BOTH_TOPTABLE_AND_FLAMEGRAPH } from './constants.mjs';
import { ViewMode, PaneView, SelectedView } from './types.mjs';

"use strict";
function isNewUI(props) {
  return "enableNewUI" in props && props.enableNewUI === true;
}
const FlameGraphHeader = (props) => {
  var _a, _b, _c;
  const styles = useStyles2(getStyles);
  const [localSearch, setLocalSearch] = useSearchInput(props.search, props.setSearch);
  const suffix = localSearch !== "" ? /* @__PURE__ */ jsx(
    Button,
    {
      icon: "times",
      fill: "text",
      size: "sm",
      onClick: () => {
        props.setSearch("");
        setLocalSearch("");
      },
      children: "Clear"
    }
  ) : null;
  if (isNewUI(props)) {
    const effectiveViewMode = props.canShowSplitView ? props.viewMode : ViewMode.Single;
    return /* @__PURE__ */ jsxs("div", { className: cx(styles.header, styles.headerNew, { [styles.stickyHeader]: props.stickyHeader }), children: [
      /* @__PURE__ */ jsx("div", { className: styles.inputContainerNew, children: /* @__PURE__ */ jsx(
        Input,
        {
          value: localSearch || "",
          onChange: (v) => {
            setLocalSearch(v.currentTarget.value);
          },
          placeholder: "Search...",
          suffix
        }
      ) }),
      effectiveViewMode === ViewMode.Split && /* @__PURE__ */ jsx("div", { className: styles.middleContainer, children: props.containerWidth >= MIN_WIDTH_TO_SHOW_SPLIT_PANE_SELECTORS ? /* @__PURE__ */ jsxs(Fragment, { children: [
        /* @__PURE__ */ jsx(
          RadioButtonGroup,
          {
            size: "sm",
            options: paneViewOptions,
            value: props.leftPaneView,
            onChange: props.setLeftPaneView,
            className: styles.buttonSpacing
          }
        ),
        /* @__PURE__ */ jsx(IconButton, { name: "exchange-alt", size: "sm", tooltip: "Swap views", onClick: props.onSwapPanes }),
        /* @__PURE__ */ jsx(
          RadioButtonGroup,
          {
            size: "sm",
            options: paneViewOptions,
            value: props.rightPaneView,
            onChange: props.setRightPaneView,
            className: styles.buttonSpacing
          }
        )
      ] }) : /* @__PURE__ */ jsxs(Fragment, { children: [
        /* @__PURE__ */ jsx(
          Dropdown,
          {
            overlay: /* @__PURE__ */ jsx(Menu, { children: paneViewOptions.map((option) => {
              var _a2;
              return /* @__PURE__ */ jsx(
                Menu.Item,
                {
                  label: (_a2 = option.label) != null ? _a2 : "",
                  active: props.leftPaneView === option.value,
                  onClick: () => option.value && props.setLeftPaneView(option.value)
                },
                option.value
              );
            }) }),
            children: /* @__PURE__ */ jsx(Button, { variant: "secondary", size: "sm", className: styles.paneDropdownButton, children: (_a = paneViewOptions.find((o) => o.value === props.leftPaneView)) == null ? void 0 : _a.label })
          }
        ),
        /* @__PURE__ */ jsx(IconButton, { name: "exchange-alt", size: "sm", tooltip: "Swap views", onClick: props.onSwapPanes }),
        /* @__PURE__ */ jsx(
          Dropdown,
          {
            overlay: /* @__PURE__ */ jsx(Menu, { children: paneViewOptions.map((option) => {
              var _a2;
              return /* @__PURE__ */ jsx(
                Menu.Item,
                {
                  label: (_a2 = option.label) != null ? _a2 : "",
                  active: props.rightPaneView === option.value,
                  onClick: () => option.value && props.setRightPaneView(option.value)
                },
                option.value
              );
            }) }),
            children: /* @__PURE__ */ jsx(Button, { variant: "secondary", size: "sm", className: styles.paneDropdownButton, children: (_b = paneViewOptions.find((o) => o.value === props.rightPaneView)) == null ? void 0 : _b.label })
          }
        )
      ] }) }),
      /* @__PURE__ */ jsxs("div", { className: styles.rightContainer, children: [
        !!((_c = props.assistantContext) == null ? void 0 : _c.length) && /* @__PURE__ */ jsx("div", { className: styles.buttonSpacing, children: /* @__PURE__ */ jsx(
          OpenAssistantButton,
          {
            origin: "grafana/flame-graph",
            prompt: "Analyze this flamegraph by querying the current datasource",
            context: props.assistantContext
          }
        ) }),
        props.showResetButton && /* @__PURE__ */ jsx(
          Button,
          {
            variant: "secondary",
            fill: "outline",
            size: "sm",
            icon: "history-alt",
            tooltip: "Reset focus and sandwich state",
            onClick: () => {
              props.onReset();
            },
            className: styles.buttonSpacing,
            "aria-label": "Reset focus and sandwich state"
          }
        ),
        effectiveViewMode === ViewMode.Single && /* @__PURE__ */ jsx(
          RadioButtonGroup,
          {
            size: "sm",
            options: paneViewOptions,
            value: props.singleView,
            onChange: props.setSingleView,
            className: styles.buttonSpacing
          }
        ),
        props.canShowSplitView && /* @__PURE__ */ jsx(
          RadioButtonGroup,
          {
            size: "sm",
            options: viewModeOptions,
            value: props.viewMode,
            onChange: props.setViewMode,
            className: styles.buttonSpacing
          }
        ),
        props.extraHeaderElements && /* @__PURE__ */ jsx("div", { className: styles.extraElements, children: props.extraHeaderElements })
      ] })
    ] });
  }
  const {
    selectedView,
    setSelectedView,
    containerWidth,
    onReset,
    textAlign,
    onTextAlignChange,
    showResetButton,
    colorScheme,
    onColorSchemeChange,
    stickyHeader,
    extraHeaderElements,
    vertical,
    isDiffMode,
    setCollapsedMap,
    collapsedMap,
    assistantContext
  } = props;
  return /* @__PURE__ */ jsxs("div", { className: cx(styles.header, { [styles.stickyHeader]: stickyHeader }), children: [
    /* @__PURE__ */ jsx("div", { className: styles.inputContainer, children: /* @__PURE__ */ jsx(
      Input,
      {
        value: localSearch || "",
        onChange: (v) => {
          setLocalSearch(v.currentTarget.value);
        },
        placeholder: "Search...",
        suffix
      }
    ) }),
    /* @__PURE__ */ jsxs("div", { className: styles.rightContainer, children: [
      !!(assistantContext == null ? void 0 : assistantContext.length) && /* @__PURE__ */ jsx("div", { className: styles.buttonSpacing, children: /* @__PURE__ */ jsx(
        OpenAssistantButton,
        {
          origin: "grafana/flame-graph",
          prompt: "Analyze this flamegraph by querying the current datasource",
          context: assistantContext
        }
      ) }),
      showResetButton && /* @__PURE__ */ jsx(
        Button,
        {
          variant: "secondary",
          fill: "outline",
          size: "sm",
          icon: "history-alt",
          tooltip: "Reset focus and sandwich state",
          onClick: () => {
            onReset();
          },
          className: styles.buttonSpacing,
          "aria-label": "Reset focus and sandwich state"
        }
      ),
      /* @__PURE__ */ jsx(ColorSchemeButton, { value: colorScheme, onChange: onColorSchemeChange, isDiffMode }),
      /* @__PURE__ */ jsxs(ButtonGroup, { className: styles.buttonSpacing, children: [
        /* @__PURE__ */ jsx(
          Button,
          {
            variant: "secondary",
            fill: "outline",
            size: "sm",
            tooltip: "Expand all groups",
            onClick: () => {
              setCollapsedMap(collapsedMap.setAllCollapsedStatus(false));
            },
            "aria-label": "Expand all groups",
            icon: "angle-double-down",
            disabled: selectedView === SelectedView.TopTable
          }
        ),
        /* @__PURE__ */ jsx(
          Button,
          {
            variant: "secondary",
            fill: "outline",
            size: "sm",
            tooltip: "Collapse all groups",
            onClick: () => {
              setCollapsedMap(collapsedMap.setAllCollapsedStatus(true));
            },
            "aria-label": "Collapse all groups",
            icon: "angle-double-up",
            disabled: selectedView === SelectedView.TopTable
          }
        )
      ] }),
      /* @__PURE__ */ jsx(
        RadioButtonGroup,
        {
          size: "sm",
          disabled: selectedView === SelectedView.TopTable,
          options: alignOptions,
          value: textAlign,
          onChange: onTextAlignChange,
          className: styles.buttonSpacing
        }
      ),
      /* @__PURE__ */ jsx(
        RadioButtonGroup,
        {
          size: "sm",
          options: getViewOptions(containerWidth, vertical),
          value: selectedView,
          onChange: setSelectedView
        }
      ),
      extraHeaderElements && /* @__PURE__ */ jsx("div", { className: styles.extraElements, children: extraHeaderElements })
    ] })
  ] });
};
const alignOptions = [
  { value: "left", description: "Align text left", icon: "align-left" },
  { value: "right", description: "Align text right", icon: "align-right" }
];
const viewModeOptions = [
  { value: ViewMode.Single, label: "Single", description: "Single view" },
  { value: ViewMode.Split, label: "Split", description: "Split view" }
];
const paneViewOptions = [
  { value: PaneView.TopTable, label: "Top Table" },
  { value: PaneView.FlameGraph, label: "Flame Graph" },
  { value: PaneView.CallTree, label: "Call Tree" }
];
function getViewOptions(width, vertical) {
  let viewOptions = [
    { value: SelectedView.TopTable, label: "Top Table", description: "Only show top table" },
    { value: SelectedView.FlameGraph, label: "Flame Graph", description: "Only show flame graph" }
  ];
  if (width >= MIN_WIDTH_TO_SHOW_BOTH_TOPTABLE_AND_FLAMEGRAPH || vertical) {
    viewOptions.push({
      value: SelectedView.Both,
      label: "Both",
      description: "Show both the top table and flame graph"
    });
  }
  return viewOptions;
}
function useSearchInput(search, setSearch) {
  const [localSearchState, setLocalSearchState] = useState(search);
  const prevSearch = usePrevious(search);
  useDebounce(
    () => {
      setSearch(localSearchState);
    },
    250,
    [localSearchState]
  );
  useEffect(() => {
    if (prevSearch !== search && search !== localSearchState) {
      setLocalSearchState(search);
    }
  }, [search, prevSearch, localSearchState]);
  return [localSearchState, setLocalSearchState];
}
const getStyles = (theme) => ({
  header: css({
    label: "header",
    display: "flex",
    flexWrap: "wrap",
    justifyContent: "space-between",
    width: "100%",
    top: 0,
    gap: theme.spacing(1),
    marginTop: theme.spacing(1)
  }),
  headerNew: css({
    label: "headerNew",
    alignItems: "flex-start",
    position: "relative"
  }),
  stickyHeader: css({
    zIndex: theme.zIndex.navbarFixed,
    position: "sticky",
    background: theme.colors.background.primary
  }),
  inputContainer: css({
    label: "inputContainer",
    flexGrow: 1,
    minWidth: "150px",
    maxWidth: "350px"
  }),
  inputContainerNew: css({
    label: "inputContainerNew",
    flexGrow: 0,
    minWidth: "150px",
    maxWidth: "350px"
  }),
  middleContainer: css({
    label: "middleContainer",
    display: "flex",
    alignItems: "center",
    flexWrap: "wrap",
    gap: theme.spacing(1),
    position: "absolute",
    left: "50%",
    transform: "translateX(-50%)"
  }),
  rightContainer: css({
    label: "rightContainer",
    display: "flex",
    alignItems: "flex-start",
    flexWrap: "wrap"
  }),
  buttonSpacing: css({
    label: "buttonSpacing",
    marginRight: theme.spacing(1)
  }),
  resetButton: css({
    label: "resetButton",
    display: "flex",
    marginRight: theme.spacing(2)
  }),
  resetButtonIconWrapper: css({
    label: "resetButtonIcon",
    padding: "0 5px",
    color: theme.colors.text.disabled
  }),
  extraElements: css({
    label: "extraElements",
    marginLeft: theme.spacing(1)
  }),
  paneDropdownButton: css({
    label: "paneDropdownButton",
    minWidth: "95px",
    justifyContent: "center"
  })
});

export { alignOptions, FlameGraphHeader as default };
//# sourceMappingURL=FlameGraphHeader.mjs.map

import { useMemo, useEffect, useCallback, useState } from 'react';
import color from 'tinycolor2';
import { useTheme2 } from '@grafana/ui';
import { BAR_BORDER_WIDTH, LABEL_THRESHOLD, GROUP_STRIP_MARGIN_LEFT, GROUP_TEXT_OFFSET, GROUP_STRIP_WIDTH, GROUP_STRIP_PADDING, MUTE_THRESHOLD, PIXELS_PER_LEVEL, HIDE_THRESHOLD, BAR_TEXT_PADDING_LEFT } from '../constants.mjs';
import { ColorSchemeDiff, ColorScheme } from '../types.mjs';
import { getBarColorByDiff, getBarColorByValue, getBarColorByPackage } from './colors.mjs';

"use strict";
function useFlameRender(options) {
  const {
    canvasRef,
    data,
    root,
    depth,
    direction,
    wrapperWidth,
    rangeMin,
    rangeMax,
    matchedLabels,
    textAlign,
    totalViewTicks,
    totalColorTicks,
    totalTicksRight,
    colorScheme,
    focusedItemData,
    collapsedMap
  } = options;
  const { ctx, devicePixelRatio } = useSetupCanvas(canvasRef, wrapperWidth, depth);
  const theme = useTheme2();
  const mutedColor = useMemo(() => {
    const barMutedColor = color(theme.colors.background.secondary);
    return theme.isLight ? barMutedColor.darken(10).toHexString() : barMutedColor.lighten(10).toHexString();
  }, [theme]);
  const getBarColor = useColorFunction(
    totalColorTicks,
    totalTicksRight,
    colorScheme,
    theme,
    mutedColor,
    rangeMin,
    rangeMax,
    matchedLabels,
    focusedItemData ? focusedItemData.item.level : 0
  );
  const renderFunc = useRenderFunc(ctx, data, getBarColor, textAlign, collapsedMap, devicePixelRatio);
  useEffect(() => {
    if (!ctx) {
      return;
    }
    ctx.clearRect(0, 0, ctx.canvas.width, ctx.canvas.height);
    const mutedPath2D = new Path2D();
    walkTree(
      root,
      direction,
      data,
      totalViewTicks,
      rangeMin,
      rangeMax,
      wrapperWidth,
      collapsedMap,
      (item, x, y, width, height, label, muted) => {
        if (muted) {
          mutedPath2D.rect(x, y, width, height);
        } else {
          renderFunc(item, x, y, width, height, label);
        }
      },
      devicePixelRatio
    );
    ctx.fillStyle = mutedColor;
    ctx.fill(mutedPath2D);
  }, [
    ctx,
    data,
    root,
    wrapperWidth,
    rangeMin,
    rangeMax,
    totalViewTicks,
    direction,
    renderFunc,
    collapsedMap,
    mutedColor,
    devicePixelRatio
  ]);
}
function useRenderFunc(ctx, data, getBarColor, textAlign, collapsedMap, devicePixelRatio) {
  return useMemo(() => {
    if (!ctx) {
      return () => {
      };
    }
    const dpr = devicePixelRatio;
    const renderFunc = (item, x, y, width, height, label) => {
      ctx.beginPath();
      ctx.rect(x + BAR_BORDER_WIDTH * dpr, y, width, height);
      ctx.fillStyle = getBarColor(item, label, false);
      ctx.stroke();
      ctx.fill();
      const collapsedItemConfig = collapsedMap.get(item);
      let finalLabel = label;
      if (collapsedItemConfig && collapsedItemConfig.collapsed) {
        const numberOfCollapsedItems = collapsedItemConfig.items.length;
        finalLabel = `(${numberOfCollapsedItems}) ` + label;
      }
      if (width >= LABEL_THRESHOLD * dpr) {
        if (collapsedItemConfig) {
          renderLabel(
            ctx,
            data,
            finalLabel,
            item,
            width,
            textAlign === "left" ? x + (GROUP_STRIP_MARGIN_LEFT + GROUP_TEXT_OFFSET) * dpr : x,
            y,
            textAlign,
            dpr
          );
          renderGroupingStrip(ctx, x, y, height, item, collapsedItemConfig, dpr);
        } else {
          renderLabel(ctx, data, finalLabel, item, width, x, y, textAlign, dpr);
        }
      }
    };
    return renderFunc;
  }, [ctx, getBarColor, textAlign, data, collapsedMap, devicePixelRatio]);
}
function renderGroupingStrip(ctx, x, y, height, item, collapsedItemConfig, dpr) {
  const groupStripX = x + GROUP_STRIP_MARGIN_LEFT * dpr;
  ctx.beginPath();
  ctx.rect(x, y, groupStripX - x + (GROUP_STRIP_WIDTH + GROUP_STRIP_PADDING) * dpr, height);
  ctx.fill();
  ctx.beginPath();
  if (collapsedItemConfig.collapsed) {
    ctx.rect(groupStripX, y + height / 4, GROUP_STRIP_WIDTH * dpr, height / 2);
  } else {
    if (collapsedItemConfig.items[0] === item) {
      ctx.rect(groupStripX, y + height / 2, GROUP_STRIP_WIDTH * dpr, height / 2);
    } else if (collapsedItemConfig.items[collapsedItemConfig.items.length - 1] === item) {
      ctx.rect(groupStripX, y, GROUP_STRIP_WIDTH * dpr, height / 2);
    } else {
      ctx.rect(groupStripX, y, GROUP_STRIP_WIDTH * dpr, height);
    }
  }
  ctx.fillStyle = "#666";
  ctx.fill();
}
function walkTree(root, direction, data, totalViewTicks, rangeMin, rangeMax, wrapperWidth, collapsedMap, renderFunc, devicePixelRatio = window.devicePixelRatio) {
  const stack = [];
  stack.push({ item: root, levelOffset: 0 });
  const dpr = devicePixelRatio;
  const pixelsPerTick = wrapperWidth * dpr / totalViewTicks / (rangeMax - rangeMin);
  let collapsedItemRendered = void 0;
  while (stack.length > 0) {
    const { item, levelOffset } = stack.shift();
    let curBarTicks = item.value;
    const muted = curBarTicks * pixelsPerTick <= MUTE_THRESHOLD * dpr;
    const width = curBarTicks * pixelsPerTick - (muted ? 0 : BAR_BORDER_WIDTH * dpr * 2);
    const height = PIXELS_PER_LEVEL * dpr;
    if (width < HIDE_THRESHOLD * dpr) {
      continue;
    }
    let offsetModifier = 0;
    let skipRender = false;
    const collapsedItemConfig = collapsedMap.get(item);
    const isCollapsedItem = collapsedItemConfig && collapsedItemConfig.collapsed;
    if (isCollapsedItem) {
      if (collapsedItemRendered === collapsedItemConfig.items[0]) {
        offsetModifier = direction === "children" ? -1 : 1;
        skipRender = true;
      } else {
        collapsedItemRendered = void 0;
      }
    } else {
      collapsedItemRendered = void 0;
    }
    if (!skipRender) {
      const barX = getBarX(item.start, totalViewTicks, rangeMin, pixelsPerTick);
      const barY = (item.level + levelOffset) * PIXELS_PER_LEVEL * dpr;
      let label = data.getLabel(item.itemIndexes[0]);
      if (isCollapsedItem) {
        collapsedItemRendered = item;
      }
      renderFunc(item, barX, barY, width, height, label, muted);
    }
    const nextList = direction === "children" ? item.children : item.parents;
    if (nextList) {
      stack.unshift(...nextList.map((c) => ({ item: c, levelOffset: levelOffset + offsetModifier })));
    }
  }
}
function useColorFunction(totalTicks, totalTicksRight, colorScheme, theme, mutedColor, rangeMin, rangeMax, matchedLabels, topLevel) {
  return useCallback(
    function getColor(item, label, muted) {
      if (muted && !matchedLabels) {
        return mutedColor;
      }
      const barColor = item.valueRight !== void 0 && (colorScheme === ColorSchemeDiff.Default || colorScheme === ColorSchemeDiff.DiffColorBlind) ? getBarColorByDiff(item.value, item.valueRight, totalTicks, totalTicksRight, colorScheme) : colorScheme === ColorScheme.ValueBased ? getBarColorByValue(item.value, totalTicks, rangeMin, rangeMax) : getBarColorByPackage(label, theme);
      if (matchedLabels) {
        return matchedLabels.has(label) ? barColor.toHslString() : mutedColor;
      }
      return item.level > topLevel - 1 ? barColor.toHslString() : barColor.lighten(15).toHslString();
    },
    [totalTicks, totalTicksRight, colorScheme, theme, rangeMin, rangeMax, matchedLabels, topLevel, mutedColor]
  );
}
function useDevicePixelRatio() {
  const [devicePixelRatio, setDevicePixelRatio] = useState(window.devicePixelRatio);
  useEffect(() => {
    var _a;
    function onChange() {
      setDevicePixelRatio(window.devicePixelRatio);
    }
    const mq = window.matchMedia(`(resolution: ${devicePixelRatio}dppx)`);
    mq.addEventListener("change", onChange);
    (_a = window.visualViewport) == null ? void 0 : _a.addEventListener("resize", onChange);
    return () => {
      var _a2;
      mq.removeEventListener("change", onChange);
      (_a2 = window.visualViewport) == null ? void 0 : _a2.removeEventListener("resize", onChange);
    };
  }, [devicePixelRatio]);
  return devicePixelRatio;
}
function useSetupCanvas(canvasRef, wrapperWidth, numberOfLevels) {
  const [ctx, setCtx] = useState();
  const devicePixelRatio = useDevicePixelRatio();
  useEffect(() => {
    if (!(numberOfLevels && canvasRef.current)) {
      return;
    }
    const ctx2 = canvasRef.current.getContext("2d");
    const height = PIXELS_PER_LEVEL * devicePixelRatio * numberOfLevels;
    canvasRef.current.width = Math.round(wrapperWidth * devicePixelRatio);
    canvasRef.current.height = Math.round(height);
    canvasRef.current.style.width = `${wrapperWidth}px`;
    canvasRef.current.style.height = `${height / devicePixelRatio}px`;
    ctx2.textBaseline = "middle";
    ctx2.font = 12 * devicePixelRatio + "px monospace";
    ctx2.strokeStyle = "white";
    setCtx(ctx2);
  }, [canvasRef, setCtx, wrapperWidth, numberOfLevels, devicePixelRatio]);
  return { ctx, devicePixelRatio };
}
function renderLabel(ctx, data, label, item, width, x, y, textAlign, dpr) {
  ctx.save();
  ctx.clip();
  ctx.fillStyle = "#222";
  const displayValue = data.valueDisplayProcessor(item.value);
  const unit = displayValue.suffix ? displayValue.text + displayValue.suffix : displayValue.text;
  const measure = ctx.measureText(label);
  const spaceForTextInRect = width - BAR_TEXT_PADDING_LEFT * dpr;
  let fullLabel = `${label} (${unit})`;
  let labelX = Math.max(x, 0) + BAR_TEXT_PADDING_LEFT * dpr;
  if (measure.width > spaceForTextInRect) {
    ctx.textAlign = textAlign;
    if (textAlign === "right") {
      fullLabel = label;
      labelX = x + width - BAR_TEXT_PADDING_LEFT * dpr;
    }
  }
  ctx.fillText(fullLabel, labelX, y + PIXELS_PER_LEVEL * dpr / 2 + 2 * dpr);
  ctx.restore();
}
function getBarX(offset, totalTicks, rangeMin, pixelsPerTick) {
  return (offset - totalTicks * rangeMin) * pixelsPerTick;
}

export { getBarX, useFlameRender, walkTree };
//# sourceMappingURL=rendering.mjs.map

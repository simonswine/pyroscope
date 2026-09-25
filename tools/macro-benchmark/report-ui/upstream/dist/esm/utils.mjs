"use strict";
function getAssistantContextFromDataFrame(data) {
  var _a, _b;
  return ((_b = (_a = data.meta) == null ? void 0 : _a.custom) == null ? void 0 : _b.assistantContext) || [];
}

export { getAssistantContextFromDataFrame };
//# sourceMappingURL=utils.mjs.map

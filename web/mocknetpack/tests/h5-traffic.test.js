"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

function element() {
  return {
    innerHTML: "",
    children: [],
    appendChild(child) { this.children.push(child); },
    querySelector() { return {}; },
    querySelectorAll() { return []; },
  };
}

const elements = { trafficInfo: element(), trafficList: element() };
const previewURLs = [];
const context = vm.createContext({
  document: {
    createElement: element,
    getElementById: id => elements[id],
    querySelectorAll: () => [],
  },
  detail: { sessionId: "session" },
  trafficFilter: "",
  bodyHtml: (_, options) => { previewURLs.push(options.url); return ""; },
  bindJsonTree() {},
});
for (const file of ["utils.js", "traffic.js"]) {
  vm.runInContext(fs.readFileSync(path.join(__dirname, "..", file), "utf8"), context, { filename: file });
}

const native = {
  id: "native", method: "GET", url: "https://example.test/health", path: "/health",
  statusCode: 200, durationMs: 12, timestamp: "2026-11-11T00:00:00Z",
};
const fetchEntry = { ...native, id: "h5", adapter: "wkwebview-fetch" };
const xhrEntry = { ...native, id: "xhr", adapter: "wkwebview-xhr" };
context.renderTraffic([fetchEntry, xhrEntry, native]);
const [fetchRow, xhrRow, nativeRow] = elements.trafficList.children;
assert.match(fetchRow.innerHTML, /H5 · fetch/);
assert.match(xhrRow.innerHTML, /H5 · XHR/);
assert.doesNotMatch(nativeRow.innerHTML, /H5/);

const details = element();
const mockButton = {};
details.querySelector = selector => selector === "#mockThisBtn" ? mockButton : {};
details.id = "historyDetail";
context.renderTrafficDetail(fetchEntry, details);
assert.match(details.innerHTML, /至 fetch Promise 完成/);
assert.match(details.innerHTML, /mockThisBtn/);
context.renderTrafficDetail(xhrEntry, details);
assert.match(details.innerHTML, /至 XHR 终态/);
assert.match(details.innerHTML, /mockThisBtn/);
assert.deepEqual(previewURLs.splice(0), [native.url, native.url, native.url, native.url]);

context.renderTrafficDetail(native, details);
assert.match(details.innerHTML, /mockThisBtn/);
assert.deepEqual(previewURLs, [native.url, native.url]);

const beacon = { ...native, id: "beacon", adapter: "wkwebview-beacon", statusCode: null, terminalStage: "h5BeaconQueued" };
const stream = { ...beacon, id: "sse", adapter: "wkwebview-eventsource", terminalStage: "h5SSEOpen" };
const resource = { ...beacon, id: "resource", adapter: "wkwebview-resource",
  durationMs: 0, terminalStage: "h5ResourceError", error: "CSP: img-src" };
const navigation = { ...beacon, id: "doc", adapter: "wkwebview-navigation",
  statusCode: 200, terminalStage: "h5NavigationFailed", error: "连接中断",
  errorDomain: "NSURLErrorDomain", errorCode: -1005 };
elements.trafficList.children = [];
context.renderTraffic([beacon, stream, resource, navigation, native]);
const [beaconRow, streamRow, resourceRow, documentRow] = elements.trafficList.children;
assert.match(beaconRow.innerHTML, /H5 · Beacon.*已排队/);
assert.match(streamRow.innerHTML, /H5 · EventSource.*已连接/);
assert.match(resourceRow.innerHTML, /H5 · 页面资源.*加载失败.*—/);
assert.match(documentRow.innerHTML, /H5 · 主文档.*200 · WebKit 主文档导航失败.*导航失败/);
context.renderTrafficDetail(navigation, details);
assert.match(details.innerHTML, /200 · WebKit 主文档导航失败：连接中断/);
assert.match(details.innerHTML, /NSURLErrorDomain \/ -1005/);
assert.doesNotMatch(details.innerHTML, /mockThisBtn/);
assert.match(context.h5ResponseStatus(resource), /CSP: img-src/);

const observed = { ...beacon, adapter: "wkwebview-resource", terminalStage: "h5ResourceObserved" };
assert.equal(context.h5ResponseStatus(observed), "已观测到资源，HTTP 状态码不可用");
assert.equal(context.h5ResponseStatus({ ...observed, statusCode: 200 }), "200 · 已观测到资源");

const failure = { ...xhrEntry, statusCode: null };
let draft;
context.mockThisRequest = (entry, fromHistory) => { draft = { entry, fromHistory }; };
context.renderTrafficDetail(failure, details);
assert.equal(mockButton.disabled, false);
mockButton.onclick();
assert.equal(draft.entry, failure);
assert.equal(draft.fromHistory, true);
context.renderTrafficDetail({ ...xhrEntry, mocked: true, mockHitVerified: true, matchedRuleId: "rule<recorded>" }, details);
assert.match(details.innerHTML, /d-v">是 · 规则 ID.*rule&lt;recorded&gt;/);
assert.match(context.trafficMockHtml({ mocked: true, matchedRuleId: "rule-from-share" }), /设备上报，未核验.*rule-from-share/);
assert.equal(context.trafficMockHtml({ mocked: false, matchedRuleId: "invalid-rule" }), "");

console.log("h5-traffic.test.js passed");

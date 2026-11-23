import { app } from '../../scripts/app.js';
import { api } from '../../scripts/api.js';
import { FLAGS } from './_flags.js';
import { formatTokenSpeed } from './lib/token_speed_format.js';

const states = new WeakMap();
const visible = new Set();

function stateFor(node) {
  if (states.has(node)) return states.get(node);
  const root = document.createElement('div');
  root.style.cssText = 'height:100%;overflow:auto;white-space:pre-wrap;padding:9px;box-sizing:border-box;background:#18252b;color:#d5edf4;border-radius:6px;font:12px/1.6 sans-serif;';
  root.textContent = 'Token速度：运行后显示请求速度、用量、首内容等待和失败次数。';
  const widget = node.addDOMWidget('kkgo_token_speed', 'kkgo_token_speed', root, {
    serialize: false,
    hideOnZoom: false,
    // The property panel's legacy renderer mutates the same widget's y/width.
    // Keep this DOM status display on its owning canvas node only.
    surfaces: { canvas: 'shown', vueNode: 'never', panel: 'never' },
    getMinHeight: () => 190,
    getMaxHeight: () => 190,
    getHeight: () => 190,
  });
  widget.serialize = false;
  widget.options.serialize = false;
  // Older frontends use computeSize; current DOM widgets use layout options.
  if (!widget.computeLayoutSize) widget.computeSize = () => [0, 190];
  const state = { node, root, data: null };
  states.set(node, state);
  visible.add(state);
  return state;
}

function render(state, data) {
  if (state.data && data.updated_at < state.data.updated_at) return;
  state.data = data;
  state.root.textContent = formatTokenSpeed(data);
}

app.registerExtension({
  name: 'KKGO.FullVideo.TokenSpeed',
  setup() {
    if (!FLAGS.nodeUi) return;
    api.addEventListener('kkgo_token_speed', ({ detail }) => {
      const node = app.graph?.getNodeById(detail.node_id);
      if (node && ['KKGO_FullVideoAgent', 'KKGO_VideoSegments'].includes(node.comfyClass || node.type)) render(stateFor(node), detail);
    });
    setInterval(() => {
      for (const state of visible) {
        if (state.data?.status === 'running') state.root.textContent = formatTokenSpeed(state.data);
      }
    }, 1000);
  },
  beforeRegisterNodeDef(nodeType, nodeData) {
    if (!FLAGS.nodeUi || !['KKGO_FullVideoAgent', 'KKGO_VideoSegments'].includes(nodeData.name)) return;
    const created = nodeType.prototype.onNodeCreated;
    nodeType.prototype.onNodeCreated = function () {
      const result = created?.apply(this, arguments);
      stateFor(this);
      const size = this.computeSize();
      this.setSize([Math.max(this.size[0], size[0]), Math.max(this.size[1], size[1])]);
      return result;
    };
    const configure = nodeType.prototype.onConfigure;
    nodeType.prototype.onConfigure = function () {
      const result = configure?.apply(this, arguments);
      const state = stateFor(this);
      const requestedAt = Date.now() / 1000;
      api.fetchApi(`/kkagent_go/token_speed?${new URLSearchParams({ node_id: this.id })}`)
        .then(response => response.ok ? response.json() : null)
        .then(payload => {
          if (payload?.result && (!state.data || state.data.updated_at <= requestedAt)) render(state, payload.result);
        }).catch(() => {});
      return result;
    };
    const removed = nodeType.prototype.onRemoved;
    nodeType.prototype.onRemoved = function () {
      visible.delete(states.get(this));
      states.delete(this);
      return removed?.apply(this, arguments);
    };
  },
});

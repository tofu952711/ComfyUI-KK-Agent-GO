const PROTOCOLS = new Set(["deepseek-messages", "openai-completions", "codez"]);
const EFFORTS = new Set(["high", "medium", "low", "auto"]);

// Give each prompt its own minimum instead of sharing a tiny editor area.
export function resizeFullVideoInputs(node) {
  for (const [name, height] of [["requirement", 120], ["rules_text", 260], ["segment_rules_text", 140], ["chain_b_reverse_rules_text", 260], ["chain_b_review_rules_text", 260], ["chain_a_review_rules_text", 260]]) {
    const widget = node.widgets?.find((item) => item.name === name);
    if (!widget) continue;
    widget.options ??= {};
    widget.options.getMinHeight = () => height;
    widget.options.getHeight = () => height;
    const element = widget.element ?? widget.inputEl;
    element?.style?.setProperty("--comfy-widget-min-height", `${height}px`);
    element?.style?.setProperty("--comfy-widget-height", `${height}px`);
    // Legacy ComfyUI uses computeSize instead of computeLayoutSize.
    if (!widget.computeLayoutSize && !widget.dshExpandedInput) {
      const computeSize = widget.computeSize;
      widget.computeSize = function (width) {
        if (this.type === "hidden" || this.type === "converted-widget") {
          return computeSize?.call(this, width) ?? [0, -4];
        }
        return [width ?? 0, height];
      };
      widget.dshExpandedInput = true;
    }
  }
  if (node.computeSize && node.setSize && node.size) {
    const minimum = node.computeSize();
    node.setSize([Math.max(node.size[0], minimum[0], 520), Math.max(node.size[1], minimum[1])]);
    node.graph?.setDirtyCanvas(true, true);
  }
}

// Repair historical field orders without altering the source saved array.
export function migrateFullVideoWidgets(values) {
  if (!Array.isArray(values) || values.length < 9) return values;
  let migrated = values;
  // The briefly released required-rules schema put rules before optional ASR.
  const rules = values[6];
  if (typeof rules === "string" && (!rules.trim() || rules.includes("\n")) &&
      PROTOCOLS.has(values[7]) && PROTOCOLS.has(values[8])) {
    migrated = [...values];
    migrated[6] = ""; // Use the bundled SenseVoice model.
    migrated[7] = values[8]; // Preserve the protocol actually used by this graph.
    migrated[8] = rules;
  }
  // B reverse rules were inserted before effort and usage. Require the complete
  // old tail shape: new rules may themselves be the literal text "high".
  if (EFFORTS.has(migrated[11]) && (migrated.length === 12 ||
      (migrated.length === 13 && typeof migrated[12] === "boolean"))) {
    migrated = [...migrated.slice(0, 11), "", ...migrated.slice(11)];
  }
  return migrated;
}

export function migrateFullVideoImageInputs(node, links = []) {
  if (!Array.isArray(node?.inputs)) return;
  for (const input of node.inputs) {
    if (input.name === "person") input.name = "image1";
    if (input.name === "product") input.name = "image2";
    if (/^image(?:10|[1-9])$/.test(input.name)) {
      input.label = input.name;
      input.localized_name = input.name;
    }
  }
  const existing = new Map(node.inputs.map((input) => [input.name, input]));
  const images = Array.from({ length: 10 }, (_, i) => existing.get(`image${i + 1}`) ||
    { name: `image${i + 1}`, label: `image${i + 1}`, localized_name: `image${i + 1}`, type: "IMAGE", link: null });
  node.inputs = [existing.get("video"), ...images,
    ...node.inputs.filter((input) => input.name !== "video" && !/^image(?:10|[1-9])$/.test(input.name))].filter(Boolean);
  const allLinks = Array.isArray(links) ? links : Object.values(links || {});
  node.inputs.forEach((input, slot) => {
    if (input.link == null) return;
    const link = allLinks.find((item) => String(Array.isArray(item) ? item[0] : item.id) === String(input.link));
    if (!link) return;
    if (Array.isArray(link)) link[4] = slot;
    else link.target_slot = slot;
  });
}

export function migrateFullVideoOutputs(node) {
  if (!Array.isArray(node?.outputs)) return;
  for (let index = 1; index <= 5; index++) {
    const slot = index + 2;
    if (!node.outputs[slot]) node.outputs.push({ name: `video${index}`, type: "VIDEO", links: null, slot_index: slot });
  }
}

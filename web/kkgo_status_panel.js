import { app } from "../../scripts/app.js";
import { api } from "../../scripts/api.js";

app.registerExtension({
  name: "KKGO.Standalone.StatusPanel",
  setup() {
    const launcher = document.createElement("button");
    launcher.textContent = "KK Go";
    launcher.title = "KK Go Agent 状态；系统提示词和接口在节点中填写";
    launcher.style.cssText = "position:fixed;right:18px;bottom:160px;z-index:9999;background:#143b40;color:#ddffff;border:1px solid #28747c;border-radius:8px;padding:9px 12px;cursor:pointer";
    const panel = document.createElement("div");
    panel.style.cssText = "display:none;position:fixed;right:18px;bottom:207px;z-index:9999;width:340px;max-height:60vh;overflow:auto;background:#152125;color:#e3f5f5;border:1px solid #28747c;border-radius:10px;padding:16px;font:13px/1.6 sans-serif;box-shadow:0 8px 30px #0008";
    const title = document.createElement("strong"); title.textContent = "KK Agent GO";
    const close = document.createElement("button"); close.textContent = "关闭";close.style.float = "right";close.onclick = () => {panel.style.display="none";};
    const explanation = document.createElement("p");
    explanation.textContent = "API Key、模型、本次用户要求，以及 A 系统提示词、B 分段/反推/审核规则，均在 KK Go 节点中填写。";
    const state = document.createElement("pre");state.style.cssText="white-space:pre-wrap;overflow-wrap:anywhere";
    panel.append(title, close, explanation, state);
    async function refresh() {
      state.textContent = "正在读取本地 Go 状态…";
      try {const response=await api.fetchApi("/kkagent_go/status");if(!response.ok)throw new Error(`HTTP ${response.status}`);const data=await response.json();state.textContent=`后端：Go\n服务：${data.runtime_ready ? "已启动" : "未启动；首次执行节点时自动启动"}\n节点：KK agent 视频反推 GO\n内部 ID：KKGO_VideoSegments\n独立节点 ID：KKGO_VideoReverse\n\n标题与输入框复刻原版；本插件与原插件分别注册。`;}
      catch(error){state.textContent=`状态读取失败：${error.message}`;}
    }
    launcher.onclick = () => {panel.style.display=panel.style.display==="none"?"block":"none";if(panel.style.display!=="none")refresh();};
    document.body.append(launcher,panel);
  }
});

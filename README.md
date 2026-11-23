# ComfyUI-KK-Agent-GO

这是独立的 ComfyUI 插件。节点输入、规则框及 A/B 视频反推业务来自原 KK Agent 插件，底层 Agent 运行时改为一个常驻 Go 服务。

**不依赖 DSH/Harness，不需要修改原插件，不需要安装后端补丁，不需要设置后端环境变量。** Windows x64 完整包包含 Go 可执行文件、FFmpeg 和原有 CPU 语音模型。

## 安装

1. 从 [Releases](https://github.com/tofu952711/ComfyUI-KK-Agent-GO/releases) 下载 Windows x64 完整包并解压，将整个 `ComfyUI-KKAgent-Go` 文件夹复制到你的 `ComfyUI/custom_nodes/`。GitHub 的 Source code ZIP 只含源码，不包含 Go 可执行文件、FFmpeg 和 ONNX 模型，请使用完整包安装。
2. 继续使用原来的 ComfyUI Python 环境。若该环境尚无 `sherpa-onnx`，用该环境的 Python 执行 `pip install -r requirements.txt`。
3. 重启 ComfyUI，刷新网页，在 `KK Agent GO` 分类中找到本插件提供的节点；节点名称在原版后增加 GO 后缀。

原插件可同时保留。新插件注册 `KKGO_*` 节点、独立路由和配置目录，不替换旧节点，不自动转换你的旧工作流。

## 节点和接线

| 可见节点名称 | 内部 ID | 用途 |
| --- | --- | --- |
| KK agent 视频反推 GO | `KKGO_VideoSegments` | 推荐入口；输出视频及独立反推资料 |
| KK 独立反推（与保存并行） GO | `KKGO_VideoReverse` | 接反推资料，生成提示词 |
| KK Agent 完整视频反推 GO | `KKGO_FullVideoAgent` | 单节点完整执行版本 |

```text
原生加载视频 → KK agent 视频反推 ─ video1—video5 → 原生保存视频/现有人脸打码
                                └ 反推资料 → KK 独立反推 → 文本查看/保存
```

为完全复刻原节点，输入框、默认值、端口与布局沿用原样；可见标题和分类增加 GO 后缀，可见名称统一使用 KK/GO。内部 ID 改成 `KKGO_*`，使新旧插件可以共存；节点信息中的模块来源为 `ComfyUI-KKAgent-Go`。如果旧插件也启用，添加节点时根据内部 ID 或模块来源选择新节点。

新插件不重复注册人脸打码或加载/保存节点，可继续使用 ComfyUI 原生节点和你已有的打码插件。

## 你需要填写的内容

- 原节点一样填写 API Key、Base URL、模型、接口协议和思考等级。
- 「本次用户要求」：本次任务，以及参考图用途。
- 不足 32 秒的 A 链路：「系统提示词（原 Skill 正文）」。
- 32—60 秒的 B 链路：分别填写「链路B：分段规定」「链路B：反推提示词规定」「链路B：审核规定」。
- 审核 AI 可沿用当前模型或单独填写审核接口。
- 可选连接 image1—image10 和音频绑定输入；规则框继续支持转为输入连接外部文本节点。

规则文本传到各自阶段的模型，不会自动替换为通用 DSH 提示词。系统规则空白会明确提示填写。

## Go 服务与资源

首次执行新节点时自动在后台启动 `runtime/bin/kkagent.exe`，全部新节点和子智能体复用一个 Go 服务。默认仅监听 `127.0.0.1:8191`，与独立框架默认 8190 分开。

首次使用自动生成 `runtime/config.local.json` 和本地调用 token。可在该文件调整 `max_active_runs`、`max_llm_requests`、`max_tool_workers`。修改后重启 ComfyUI 和该 Go 服务再生效。

ComfyUI 用户目录下的 `default/kkagent_bridge/` 保存本插件证据、运行结果和 Go 数据。原插件的 `default/dsh_bridge/` 不读取、不修改。

原视频准备、语音识别、说话人分析和 FFmpeg 仍在 Python/本地工具中执行。优化的是重复的 Agent 运行环境，工具的计算仍需要 CPU 和内存。

## 使用前须知

- 原工作流中的旧节点继续使用原插件；要使用 Go，在新工作流中添加内部 ID 为 `KKGO_*` 的新节点并连接素材和规则。
- B 链路保持实际裁切、统一设定、每段独立会话和最终审核。输出格式沿用当前来源版本。
- 三种接口协议有本地回放测试；真实 API 网关需用你自己的接口配置验证。
- 本包面向 Windows x64；其他系统需自行编译对应 Go 二进制并准备 FFmpeg。
- API Key 与规则仍会随 ComfyUI 工作流保存，分享工作流前清空 Key。
- 原来源许可证、FFmpeg 及模型许可证随包保留。


Go 0.1.2 按原 DSH 已实测的传输规则处理图片：单张 ≤1 MiB 保留原格式，>1 MiB 的 PNG/JPEG 使用 JPEG 85、4:2:0 编码；图片数量和尺寸不变，原始资产保留。JPEG 是有损编码，Go 编码器与 DSH 输出不逐字节相同。本地上限沿用 128 MiB，不再采用临时的整包 20 MiB 质量阶梯。

## 当前版本

v0.1.4 包含工作流会话隔离、A 链路参考视频最多 30 秒（原视频分析仍使用完整源视频）、A 链路审核，以及审核记录原文与新文相同时跳过替换的修复。

## 源码与构建

仓库根目录是 ComfyUI 插件源码，`cmd/agentd` 与 `internal/` 是 Go 运行时源码。Go 核心只依赖标准库，要求 Go 1.23 或更高版本。

Windows PowerShell：

```powershell
.\scripts\build-runtime.ps1 -Test
```

构建结果为 `runtime/bin/kkagent.exe`。其他系统可运行 `go build -trimpath -o runtime/bin/kkagent ./cmd/agentd`，并自行准备对应平台的 FFmpeg。

FFmpeg 可执行文件和 ONNX 模型不提交到 Git；Windows 完整包保留相关资源及许可证。开发时可将完整包中的 `vendor/ffmpeg/` 与 `models/` 资源放回对应目录。首次运行生成的本地配置、服务 token、会话历史和素材数据不包含在仓库及发布包中。

本次发布同步本机插件的任务停止、预处理进程隔离和取消状态显示修复。发布不附带任何用户工作流或文本框填写内容。

# Desktop / Web 体验与共享配置

**Status:** current
**Last verified:** 2026-10-11

> **Ownership:** 当前工作台的信息架构、设置交互与实现边界。
> 运行时与信任边界见 [Desktop architecture](desktop-workbench.md)，
> 操作方法见 [Desktop guide](../guides/desktop-workbench.md)。

## 决策

保留 Desktop 与 Web 共用的 WebUI，不另建框架或配置加载器。默认显示会话导航和
对话，Activity / Changes 按需展开；窄屏使用互斥抽屉并保护背景、Escape 和焦点返回。
Desktop 以打开工作区为主动作，Web 以浏览已有会话为主动作；没有会话时隐藏不可用的
输入框。读取保存历史不接续模型，只有显式发送才 attach。

采用 `project-native`：以当前 QueryEngine 和共享配置服务为权威，修正按钮承诺与
实际能力、信息层级及草稿生命周期，不复制参考产品的状态机。设计依据来自源码和
受控回归，不是用户访谈或效率提升研究。

## 三种配置不能混在一起

| 层 | 权威与生效方式 |
|---|---|
| 持久默认值 | Go `SettingsSnapshot` / `SettingsPatchContext`；user 与 project 覆盖；用于新会话 |
| 当前会话 | QueryEngine execution settings；composer 的 model / effort / access 控件 |
| 凭证 | 主机环境、既有存储或 OS 加密 Desktop vault；不经通用设置接口返回 |

TUI、CLI、Desktop 和 Web 使用同一设置服务。首批白名单是 provider、model、
已有 model_profile、api_base_url、max_turns、permission_mode、theme、reduced_motion、
prompt_suggestions。schema 返回类型、分组、默认值、来源、选项与校验元数据；
前端只拥有编辑草稿和呈现，不新增继承规则或秘密字段。

project 范围只能由主机拥有的 live session 解析。API 不接收文件路径，继续保护
bearer、同源 cookie 与 CSRF。null patch 删除本范围覆盖，false / 0 则是实际值；
revision 冲突拒绝覆盖，未知字段在文件事务中保留但不返回给前端。

## 设置动线

模型分类是默认入口；分类切换清除搜索，跨分类搜索不重建字段节点。隐藏字段校验
失败时回到其分类并聚焦。正常高度只有字段区滚动，短窗口降级为整个 dialog 滚动，
避免固定外壳遮住无效字段。凭证说明使用原生 details / summary；搜索只匹配公开说明词，
不读取账户或密钥。Desktop 的管理按钮沿用既有凭证入口与放弃草稿确认；Web 没有
不能执行的管理按钮。

`choice_only` 在目录为空时仍表示只能选择，不能变成自由文本。model profile 与
显式模型设置的互斥关系来自 Go 的 `conflicts_with`；只在显式 Save 时确认并提交同一
revision 中的对应 null 删除。`blocked_by` 只含不可编辑阻挡项名称，不泄漏值，也
不授权前端删除它。取消、失败或 409 保留草稿，继承的 effective value 不是删除目标。

Reset 是草稿，不是写操作；仅本范围存储的覆盖可进入删除状态，包括 false、0 和
空字符串。待删除时旧输入隐藏并禁用，避免无效旧值阻止 null patch。Undo 恢复先前
输入和 patch。没有 override 的编辑只能 Discard change。明确 Save / Reload 后，
仍以共享服务返回的 snapshot 为准。

## 主机与异步边界

Desktop 的 `apiResult` 用普通对象传递公开 HTTP 错误，renderer 再构造 Error，
保留 409 的 typed 冲突。它不扩大 IPC 白名单或目录选择权限；未知主机 / 网络失败只
返回固定文案。settings 的请求代际保护重载，模型默认值不会重绑已有执行。

settings GET / PATCH 的主机与 Web 请求有固定 15 秒 deadline，涵盖响应正文读取，
不改变其他 API 或 SSE。读取超时可关闭并重新打开；写入超时保留草稿并阻止再次 Save，
必须显式 Reload。超时不证明文件未写入：服务器可能已提交但确认响应尚未返回，
迟到响应不能清除草稿，Reload 才读取持久化事实。

消息 timeline 保持同会话节点身份及原生 details 状态。交互表单按 pending interaction
和草稿签名更新，队列计数独立刷新，不因无关活动摧毁正在输入的问题答案。
工作区配置由 runtime 实际解析，不能仅凭 Desktop vault 缺失就认定模型不可用。
旧 vault 是低优先级 fallback，凭证只绑定相同 provider 和完整规范化 endpoint。

## 证据与后续范围

`make desktop-check` 覆盖 Node 回归与语法；AppServer 模块图测试核对 import 路由和
MIME，执行设置测试核对同源 reasoning 选项。可选
[浏览器夹具](../../desktop/scripts/workbench_ui_acceptance.cjs) 只使用合成数据、
HTTP SSE 和隔离 context。原生共享设置 smoke 经实际 form → preload → AppServer，
用文件与新 CLI 进程核对零值、来源、冲突和 renderer reload；它使用 test-owned
配置，不是 Keychain、真实 provider、签名或发行验收。

当前 diff / HEAD 的 focused、clean committed merge 与 evidence-ready 才是本轮
本地门禁。整合候选的原生结果不能当作每个拆分 HEAD 的逐项原生证明。
Command completion、显式 prompt suggestion、Task Explorer、MCP、权限编辑、Goal
和工作台 Home 由各自后续切片交付，不由本设置界面宣称已完成。

主要 owner：

- [Settings service](../../engine/config/workbench_settings.go)：schema、合并、revision 与文件事务。
- [createSettingsView](../../internal/webui/assets/settings_view.mjs)：过滤、草稿、保存与焦点。
- [main / preload](../../desktop/main.cjs)：可信 sender、公开错误 envelope 与 OS 凭证入口。
- [transport](../../internal/webui/assets/transport.mjs)：Desktop/Web 通道与 typed 错误重建。
- [app](../../internal/webui/assets/app.mjs)：会话接入、同源设置投影和布局状态。

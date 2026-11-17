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

## 未做但值得排期的

银狐功能对照表里的核心模块（屏幕多模式、键盘记录、实时音频、远程交谈、文件/终端/注册表/服务/进程、
代理、搜索、浏览器数据、注入、生成）已全部覆盖。剩下的不是缺失模块，而是工程质量项：

- **Linux/macOS 的设备名列表**已随本轮下发；后续可加"设备热插拔"（当前需要点一次刷新）。
- **音频编码参数**（码率、声道）仍是固定的 32 kbps 单声道；如需"听音乐"可加高质量档位。

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

## 方法说明

- 文章枚举通过 WordPress REST API（`/wp-json/wp/v2/posts?per_page=100`）完成，避免抓 25 页列表。
- 只对与"远控能力"直接相关的文章逐段阅读；样本家族/勒索软件类文章仅用于功能面对照。
- 本文不复制样本哈希/IOC 之外的攻击代码，只保留"产品应该具备什么"的结论。

# SanTianShell

Windows 桌面远程管理工具，技术栈为 Go + Wails + Vite + 原生 JavaScript/CSS。

当前主要功能：

- 独立保存主机资料（Windows DPAPI 加密）
- 持续交互 SSH 终端、连接诊断、持久会话和批量同步命令
- 独立按需启动的 SFTP、目录浏览、上传、下载和远程文本编辑
- RDP、SOCKS5、SSH 隧道及 Windows 远程桌面会话管理
- CPU、内存、网络、延迟、磁盘和进程实时监控
- Sing-box 节点部署、状态刷新、备份还原和节点管理
- 支持建议、审核、托管权限的 AI 运维助手

开发运行：双击 `dev.bat`，或执行 `wails dev`。

构建：`wails build`。

安全说明：主机密码、节点凭证和 AI Key 使用当前 Windows 用户的 DPAPI 加密后保存在本机。SSH 首次手动连接时确认并保存主机指纹，后台静默刷新不会自动信任未确认的主机。

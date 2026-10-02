# SJTU-Canvas CLI

这是一个汇总了上海交通大学学习平台接口的CLI。

你可以在脚本中组合使用这些接口，实现特定任务；你也可以将其作为UI后端。

<img width="814" height="463" alt="Screenshot 2026-10-02 at 14 44 33" src="https://github.com/user-attachments/assets/012e3141-dd0a-4858-a8e6-6131562098d6" />

## 可用的操作系统

本软件仅适配macOS和使用systemd的GNU/Linux。桌面和无桌面环境下均可使用。

在桌面环境下，你的登录凭证会被存放在keychain中；安全性相对较高。

在桌面环境下，若daemon登录凭证出现问题，你会叮到咚鸡。

## 安装

```bash
curl -fsSL https://soft.mahiro.ink/sjtu-install.sh | sh
```

## 快速开始

1. **使用前登录**：
   ```bash
   sjtu auth canvas login      # 配置 Canvas API Token
   sjtu auth jaccount login    # 交互式扫码登录 jAccount
   ```

2. **按需注册常驻服务**：
   若需复用预热会话加速命令并启用健康监测，可一键注册为系统服务：
   ```bash
   sjtu daemon install
   ```

## 鸣谢

感谢MoonshotAI/Kimi-K3和Google/Gemini-3.8-Flash。

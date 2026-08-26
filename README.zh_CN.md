[English](/README.md) | [فارسی](/README.fa_IR.md) | [العربية](/README.ar_EG.md) | [中文](/README.zh_CN.md) | [Español](/README.es_ES.md) | [Русский](/README.ru_RU.md)

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./media/3x-ui-dark.png">
    <img alt="3x-ui" src="./media/3x-ui-light.png">
  </picture>
</p>

[![Release](https://img.shields.io/github/v/release/mhsanaei/3x-ui.svg)](https://github.com/MHSanaei/3x-ui/releases)
[![Build](https://img.shields.io/github/actions/workflow/status/mhsanaei/3x-ui/release.yml.svg)](https://github.com/MHSanaei/3x-ui/actions)
[![GO Version](https://img.shields.io/github/go-mod/go-version/mhsanaei/3x-ui.svg)](#)
[![Downloads](https://img.shields.io/github/downloads/mhsanaei/3x-ui/total.svg)](https://github.com/MHSanaei/3x-ui/releases/latest)
[![License](https://img.shields.io/badge/license-GPL%20V3-blue.svg?longCache=true)](https://www.gnu.org/licenses/gpl-3.0.en.html)
[![Go Reference](https://pkg.go.dev/badge/github.com/mhsanaei/3x-ui/v2.svg)](https://pkg.go.dev/github.com/mhsanaei/3x-ui/v2)
[![Go Report Card](https://goreportcard.com/badge/github.com/mhsanaei/3x-ui/v2)](https://goreportcard.com/report/github.com/mhsanaei/3x-ui/v2)

**3X-UI** — 一个基于网页的高级开源控制面板，专为管理 Xray-core 服务器而设计。它提供了用户友好的界面，用于配置和监控各种 VPN 和代理协议。

> [!IMPORTANT]
> 本项目仅用于个人使用和通信，请勿将其用于非法目的，请勿在生产环境中使用。

作为原始 X-UI 项目的增强版本，3X-UI 提供了更好的稳定性、更广泛的协议支持和额外的功能。

## 快速开始

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/jbhd135/3x-ui-jbhd/main/scripts/install-selfhosted.sh)
```

安装过程中会提示您设置面板用户名、密码、端口和访问路径。
安装完成后，脚本会输出浏览器登录网址、用户名和密码清单，方便您直接登录。

无人值守安装（用于自动迁移）：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/jbhd135/3x-ui-jbhd/main/scripts/install-selfhosted.sh) \
  --no-config-prompt --skip-start --yes
```

说明：

- 该脚本固定从 `jbhd135/3x-ui-jbhd` 的最新 GitHub Release 下载对应架构安装包。
- 安装和升级默认启用 IPv4-only：除了持久写入 sysctl，还会在 x-ui 启动前逐网卡禁用 IPv6，并由 systemd 定时复核，防止 Netplan 或云平台网络重载后重新启用 IPv6。只有明确传入 `--enable-ipv6` 才会保留 IPv6。
- 自动迁移会先执行无人值守安装，再用 Google Drive 最新恢复点覆盖数据库和证书。
- 如果服务器上没有现成数据库，脚本会进入配置流程；直接回车会使用安全随机账号、密码、端口，路径默认 `/jbhd/`。
- 如果服务器上已有 `/etc/x-ui/x-ui.db`，脚本会询问是否重新配置面板；如需跳过询问并强制重新设置，可附加 `--configure`。

## 一键迁移到当前改版

如果您已经有一台运行中的 `x-ui` 服务器，并且想把另一台服务器迁移到当前改版，可以使用仓库内的迁移脚本：

```bash
chmod +x ./scripts/migrate-3x-ui.sh
./scripts/migrate-3x-ui.sh \
  --source-host 旧服务器IP或域名 \
  --source-pass '旧服务器SSH密码' \
  --target-host 新服务器IP或域名 \
  --target-pass '新服务器SSH密码' \
  --yes
```

如果您想做成真正的“自托管一键命令”，把 [scripts/migrate-3x-ui.sh](/scripts/migrate-3x-ui.sh) 放到您自己的域名、对象存储或 GitHub Raw 后，可以直接这样运行：

```bash
bash <(curl -fsSL https://你的域名或raw地址/migrate-3x-ui.sh) \
  --source-host 旧服务器IP或域名 \
  --source-pass '旧服务器SSH密码' \
  --target-host 新服务器IP或域名 \
  --target-pass '新服务器SSH密码' \
  --yes
```

说明：

- 目标服务器如果还没有安装 `x-ui`，脚本会自动完成基础安装并迁移源服务器数据库。
- 该脚本默认保留目标服务器自己的 `/etc/x-ui/x-ui.db`；如果需要完整迁移源服务器数据，可附加 `--copy-db`。
- 全新裸机场景下，脚本会自动同步 `xray` 内核、`.dat` 文件、面板命令行工具和 systemd 服务文件。
- 如果要强制同步源服务器的 `xray` 内核和 `.dat` 文件，可附加 `--copy-xray`。
- 当前脚本已经做成单文件自举模式，可以直接用于 `curl | bash`。
- 执行脚本的本地机器需要安装 `sshpass`。

## 特别感谢

- [alireza0](https://github.com/alireza0/)

## 致谢

- [Iran v2ray rules](https://github.com/chocolate4u/Iran-v2ray-rules) (许可证: **GPL-3.0**): _增强的 v2ray/xray 和 v2ray/xray-clients 路由规则，内置伊朗域名，专注于安全性和广告拦截。_
- [Russia v2ray rules](https://github.com/runetfreedom/russia-v2ray-rules-dat) (许可证: **GPL-3.0**): _此仓库包含基于俄罗斯被阻止域名和地址数据自动更新的 V2Ray 路由规则。_

## 支持项目

**如果这个项目对您有帮助，您可以给它一个**:star2:

<a href="https://www.buymeacoffee.com/MHSanaei" target="_blank">
<img src="./media/default-yellow.png" alt="Buy Me A Coffee" style="height: 70px !important;width: 277px !important;" >
</a>

</br>
<a href="https://nowpayments.io/donation/hsanaei" target="_blank" rel="noreferrer noopener">
   <img src="./media/donation-button-black.svg" alt="Crypto donation button by NOWPayments">
</a>

## 随时间变化的星标数

[![Stargazers over time](https://starchart.cc/MHSanaei/3x-ui.svg?variant=adaptive)](https://starchart.cc/MHSanaei/3x-ui)

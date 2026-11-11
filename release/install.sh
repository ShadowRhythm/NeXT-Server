#!/bin/bash

# =========================================================
# NeXT-Server 一键安装脚本
# 项目地址: https://github.com/ShadowRhythm/NeXT-Server
# =========================================================

cur_dir=$(pwd)
REPO="ShadowRhythm/NeXT-Server"
RAW_URL="https://raw.githubusercontent.com/${REPO}/main/release"

# 检查 root 权限
[[ $EUID -ne 0 ]] && echo -e "\033[31m错误：必须使用 root 用户运行此脚本！\033[0m\n" && exit 1

# 检测操作系统
if [[ -f /etc/redhat-release ]]; then
    release="rhel"
elif cat /etc/issue 2>/dev/null | grep -Eqi "debian"; then
    release="debian"
elif cat /etc/issue 2>/dev/null | grep -Eqi "ubuntu"; then
    release="ubuntu"
elif cat /etc/issue 2>/dev/null | grep -Eqi "centos|red hat|redhat|rocky|alma|fedora"; then
    release="rhel"
elif cat /proc/version 2>/dev/null | grep -Eqi "debian"; then
    release="debian"
elif cat /proc/version 2>/dev/null | grep -Eqi "ubuntu"; then
    release="ubuntu"
elif cat /proc/version 2>/dev/null | grep -Eqi "centos|red hat|redhat|rocky|alma|fedora"; then
    release="rhel"
else
    echo -e "\033[31m未检测到支持的系统版本！\033[0m\n" && exit 1
fi

# 检测 CPU 架构
arch=$(uname -m 2>/dev/null || arch 2>/dev/null)

if [[ $arch == "x86_64" || $arch == "x64" || $arch == "amd64" ]]; then
    arch="amd64"
elif [[ $arch == "aarch64" || $arch == "arm64" ]]; then
    arch="arm64"
elif [[ $arch == "riscv64" ]]; then
    arch="riscv64"
else
    arch="amd64"
    echo -e "检测架构失败，使用默认架构: ${arch}"
fi

if [ "$(getconf WORD_BIT)" != '32' ] && [ "$(getconf LONG_BIT)" != '64' ] ; then
    echo -e "\033[31m本软件不支持 32 位系统，请使用 64 位系统 (amd64/arm64/riscv64)！\033[0m"
    exit 2
fi

echo -e "系统环境: ${release} (${arch})"

install_base() {
    if [[ x"${release}" == x"rhel" ]]; then
        dnf install epel-release -y 2>/dev/null || yum install epel-release -y 2>/dev/null
        dnf install wget curl tar crontabs -y 2>/dev/null || yum install wget curl tar crontabs -y 2>/dev/null
    else
        apt update -y
        apt install wget curl tar cron -y
    fi
}

check_status() {
    if [[ ! -f /etc/systemd/system/next-server.service ]]; then
        return 2
    fi

    temp=$(systemctl status next-server 2>/dev/null | grep Active | awk '{print $3}' | cut -d "(" -f2 | cut -d ")" -f1)

    if [[ x"${temp}" == x"running" ]]; then
        return 0
    else
        return 1
    fi
}

install_next_server() {
    local target_version="$1"

    # 获取要安装的版本
    if [[ -z "$target_version" ]]; then
        echo -e "正在获取 NeXT-Server 最新版本号..."
        target_version=$(curl -sL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name":' | head -n 1 | sed -E 's/.*"([^"]+)".*/\1/')
        if [[ -z "$target_version" ]]; then
            echo -e "\033[31m获取最新版本号失败，可能是 GitHub API 访问频次限制，请稍后重试或手动指定版本安装。\033[0m"
            exit 1
        fi
        echo -e "检测到最新版本：\033[32m${target_version}\033[0m"
    else
        echo -e "准备安装指定版本：\033[32m${target_version}\033[0m"
    fi

    mkdir -p /usr/local/next-server/
    cd /usr/local/next-server/ || exit 1

    # 尝试多种可能的 release 命名规则下载
    local download_success=0
    local dl_urls=(
        "https://github.com/${REPO}/releases/download/${target_version}/next-server-${target_version}-linux-${arch}.tar.gz"
        "https://github.com/${REPO}/releases/download/${target_version}/next-server-linux-${arch}.tar.gz"
    )

    # 如果版本带 'v' 或不带 'v'，增加容错尝试
    if [[ $target_version == v* ]]; then
        local pure_ver="${target_version#v}"
        dl_urls+=("https://github.com/${REPO}/releases/download/${target_version}/next-server-${pure_ver}-linux-${arch}.tar.gz")
        dl_urls+=("https://github.com/${REPO}/releases/download/${pure_ver}/next-server-${pure_ver}-linux-${arch}.tar.gz")
    else
        local v_ver="v${target_version}"
        dl_urls+=("https://github.com/${REPO}/releases/download/${target_version}/next-server-${v_ver}-linux-${arch}.tar.gz")
        dl_urls+=("https://github.com/${REPO}/releases/download/${v_ver}/next-server-${v_ver}-linux-${arch}.tar.gz")
    fi

    echo -e "正在下载 NeXT-Server 安装包..."
    for url in "${dl_urls[@]}"; do
        echo -e "尝试从: ${url} 下载..."
        if wget -q --timeout=30 -O /usr/local/next-server/next-server-linux.tar.gz "${url}" && [[ -s /usr/local/next-server/next-server-linux.tar.gz ]]; then
            # 校验是否真下载到了 tar.gz 而非 404 html 文本
            if tar -tzf /usr/local/next-server/next-server-linux.tar.gz >/dev/null 2>&1; then
                download_success=1
                break
            fi
        fi
        rm -f /usr/local/next-server/next-server-linux.tar.gz
    done

    if [[ $download_success -ne 1 ]]; then
        echo -e "\033[31m下载 NeXT-Server 失败！请检查版本号是否存在，以及本机与 GitHub 的网络连接。\033[0m"
        exit 1
    fi

    # 解压二进制文件
    tar -xzf next-server-linux.tar.gz
    rm -f next-server-linux.tar.gz

    # 如果解压出的二进制在子目录中，移出来
    if [[ ! -f /usr/local/next-server/next-server ]]; then
        find_bin=$(find /usr/local/next-server/ -name "next-server" -type f | head -n 1)
        if [[ -n "$find_bin" ]]; then
            mv "$find_bin" /usr/local/next-server/next-server
        fi
    fi

    if [[ ! -f /usr/local/next-server/next-server ]]; then
        echo -e "\033[31m解压后未找到 next-server 可执行文件，安装失败！\033[0m"
        exit 1
    fi

    chmod +x /usr/local/next-server/next-server

    # 创建配置与数据目录
    mkdir -p /etc/next-server/acme/

    # 下载或生成 systemd 守护进程文件
    file="${RAW_URL}/next-server.service"
    wget -q --timeout=15 -O /etc/systemd/system/next-server.service "${file}"
    if [[ $? -ne 0 || ! -s /etc/systemd/system/next-server.service ]]; then
        cat << 'EOF' > /etc/systemd/system/next-server.service
[Unit]
Description=NeXT Server Service
Documentation=https://github.com/ShadowRhythm/NeXT-Server
After=network.target nss-lookup.target
Wants=network.target

[Service]
User=root
Group=root
Type=simple
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
LimitAS=infinity
LimitRSS=infinity
LimitCORE=infinity
LimitNOFILE=65535
WorkingDirectory=/usr/local/next-server/
ExecStart=/usr/local/next-server/next-server -c /etc/next-server/config.json
Restart=on-failure
RestartSec=10s

[Install]
WantedBy=multi-user.target
EOF
    fi

    systemctl daemon-reload
    systemctl enable next-server >/dev/null 2>&1

    # 初始化默认配置文件（若不存在）
    if [[ ! -f /etc/next-server/config.json ]]; then
        cat << 'EOF' > /etc/next-server/config.json
{
  "log": {
    "level": "info"
  },
  "servers": [
    {
      "url": "https://panel.example.com",
      "key": "<node communication key>",
      "listen": "::",
      "tls": {
        "acme": {
          "email": "admin@example.com",
          "data_directory": "/etc/next-server/acme"
        }
      }
    }
  ]
}
EOF
        echo -e "已在 \033[33m/etc/next-server/config.json\033[0m 生成默认配置文件模板，请修改后再启动服务！"
    else
        systemctl restart next-server
        sleep 2
        check_status
        if [[ $? == 0 ]]; then
            echo -e "\033[32mNeXT-Server 重启成功！\033[0m"
        else
            echo -e "\033[33mNeXT-Server 重启未进入 running 状态，可使用 next-server log 查看日志。\033[0m"
        fi
    fi

    # 安装管理命令脚本 /usr/bin/next-server
    curl -sSfL -o /usr/bin/next-server "${RAW_URL}/next-server.sh"
    if [[ $? -ne 0 || ! -s /usr/bin/next-server ]]; then
        echo -e "\033[33m下载远程管理脚本失败，正在尝试本地复制...\033[0m"
        if [[ -f "${cur_dir}/release/next-server.sh" ]]; then
            cp "${cur_dir}/release/next-server.sh" /usr/bin/next-server
        fi
    fi
    chmod +x /usr/bin/next-server

    cd "$cur_dir" || true
    rm -f install.sh

    echo -e "\n\033[32m========================================================\033[0m"
    echo -e "\033[32m NeXT-Server ${target_version} 安装完成！\033[0m"
    echo -e "\033[32m========================================================\033[0m"
    echo -e "配置文件路径: /etc/next-server/config.json"
    echo -e "管理命令用法: "
    echo -e "--------------------------------------------------------"
    echo -e "  \033[36mnext-server\033[0m              - 显示管理菜单 (功能更多)"
    echo -e "  \033[36mnext-server start\033[0m        - 启动 next-server"
    echo -e "  \033[36mnext-server stop\033[0m         - 停止 next-server"
    echo -e "  \033[36mnext-server restart\033[0m      - 重启 next-server"
    echo -e "  \033[36mnext-server status\033[0m       - 查看运行状态"
    echo -e "  \033[36mnext-server log\033[0m          - 查看运行日志"
    echo -e "  \033[36mnext-server config\033[0m       - 修改配置文件"
    echo -e "  \033[36mnext-server update\033[0m       - 更新到最新版本"
    echo -e "  \033[36mnext-server update [ver]\033[0m - 更新至指定版本"
    echo -e "  \033[36mnext-server version\033[0m      - 查看版本信息"
    echo -e "  \033[36mnext-server uninstall\033[0m    - 卸载 next-server"
    echo -e "--------------------------------------------------------\n"
}

echo -e "\033[32m开始安装 NeXT-Server...\033[0m"
install_base
install_next_server "$1"

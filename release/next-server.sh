#!/bin/bash

# =========================================================
# NeXT-Server 服务管理脚本
# 项目地址: https://github.com/ShadowRhythm/NeXT-Server
# =========================================================

version="v1.0.0"
REPO="ShadowRhythm/NeXT-Server"
RAW_URL="https://raw.githubusercontent.com/${REPO}/main/release"
CONFIG_FILE="/etc/next-server/config.json"
BIN_FILE="/usr/local/next-server/next-server"

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

confirm() {
    if [[ $# -gt 1 ]]; then
        echo && read -p "$1 [默认 $2]: " temp
        if [[ x"${temp}" == x"" ]]; then
            temp=$2
        fi
    else
        read -p "$1 [y/n]: " temp
    fi
    if [[ x"${temp}" == x"y" || x"${temp}" == x"Y" ]]; then
        return 0
    else
        return 1
    fi
}

confirm_restart() {
    confirm "是否重启 next-server 以应用修改？" "y"
    if [[ $? == 0 ]]; then
        restart
    else
        show_menu
    fi
}

before_show_menu() {
    echo && echo -n -e "按回车键返回主菜单: " && read -r temp
    show_menu
}

install() {
    bash <(curl -Ls "${RAW_URL}/install.sh")
    if [[ $? == 0 ]]; then
        if [[ $# == 0 ]]; then
            start
        else
            start 0
        fi
    fi
}

update() {
    local target_ver=""
    if [[ $# -eq 0 || -z "$2" ]]; then
        echo && echo -n -e "请输入指定版本号 (留空直接回车更新至最新版): " && read -r target_ver
    else
        target_ver=$2
    fi

    bash <(curl -Ls "${RAW_URL}/install.sh") "$target_ver"

    if [[ $? == 0 ]]; then
        echo -e "\033[32m更新完成！已自动重启 next-server，可使用 next-server log 查看日志。\033[0m"
        exit 0
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

config() {
    echo "提示：保存并退出配置文件后，将自动尝试重启 next-server 服务。"
    if [[ ! -f "$CONFIG_FILE" ]]; then
        echo -e "\033[31m配置文件不存在: ${CONFIG_FILE}，请先安装 next-server！\033[0m"
        if [[ $# == 0 ]]; then before_show_menu; fi
        return 1
    fi

    # 优先使用 vim/vi 编辑
    if command -v vim >/dev/null 2>&1; then
        vim "$CONFIG_FILE"
    elif command -v nano >/dev/null 2>&1; then
        nano "$CONFIG_FILE"
    else
        vi "$CONFIG_FILE"
    fi

    sleep 1
    echo "正在重启 next-server..."
    systemctl restart next-server
    sleep 2
    check_status
    case $? in
        0)
            echo -e "\033[32mnext-server 状态: 已运行\033[0m"
            ;;
        1)
            echo -e "\033[31mnext-server 重启失败，是否立即查看日志？[Y/n]\033[0m" && echo
            read -e -p "(默认: y): " yn
            [[ -z ${yn} ]] && yn="y"
            if [[ ${yn} == [Yy] ]]; then
               show_log
            fi
            ;;
        2)
            echo -e "\033[33mnext-server 状态: 未安装\033[0m"
            ;;
    esac

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

uninstall() {
    confirm "确定要卸载 next-server 吗？所有配置文件和数据都将被删除！" "n"
    if [[ $? != 0 ]]; then
        if [[ $# == 0 ]]; then
            show_menu
        fi
        return 0
    fi
    systemctl stop next-server 2>/dev/null
    systemctl disable next-server 2>/dev/null
    rm -f /etc/systemd/system/next-server.service
    systemctl daemon-reload
    systemctl reset-failed 2>/dev/null
    rm -rf /etc/next-server/
    rm -rf /usr/local/next-server/

    echo ""
    echo -e "\033[32m卸载成功！如果想删除本管理命令脚本，请退出后运行: rm -f /usr/bin/next-server\033[0m"
    echo ""

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

start() {
    check_status
    if [[ $? == 0 ]]; then
        echo ""
        echo -e "\033[33mnext-server 已在运行中，无需重复启动。如需重启请选择重启。\033[0m"
    else
        systemctl start next-server
        sleep 2
        check_status
        if [[ $? == 0 ]]; then
            echo -e "\033[32mnext-server 启动成功，请使用 next-server log 查看运行日志。\033[0m"
        else
            echo -e "\033[31mnext-server 启动失败，请稍后使用 next-server log 查看错误信息。\033[0m"
        fi
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

stop() {
    systemctl stop next-server
    sleep 2
    check_status
    if [[ $? == 1 ]]; then
        echo -e "\033[32mnext-server 停止成功\033[0m"
    else
        echo -e "\033[31mnext-server 停止失败，请检查服务状态或日志。\033[0m"
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

restart() {
    systemctl restart next-server
    sleep 2
    check_status
    if [[ $? == 0 ]]; then
        echo -e "\033[32mnext-server 重启成功，请使用 next-server log 查看运行日志。\033[0m"
    else
        echo -e "\033[31mnext-server 重启失败，请使用 next-server log 查看日志信息。\033[0m"
    fi
    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

status() {
    systemctl status next-server --no-pager -l
    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

enable() {
    systemctl enable next-server
    if [[ $? == 0 ]]; then
        echo -e "\033[32mnext-server 设置开机自启成功\033[0m"
    else
        echo -e "\033[31mnext-server 设置开机自启失败\033[0m"
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

disable() {
    systemctl disable next-server
    if [[ $? == 0 ]]; then
        echo -e "\033[32mnext-server 取消开机自启成功\033[0m"
    else
        echo -e "\033[31mnext-server 取消开机自启失败\033[0m"
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

show_log() {
    journalctl -u next-server.service -e --no-pager -f
    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

update_shell() {
    wget -q -O /usr/bin/next-server "${RAW_URL}/next-server.sh"
    if [[ $? != 0 || ! -s /usr/bin/next-server ]]; then
        echo ""
        echo -e "\033[31m下载管理脚本失败，请检查本机与 GitHub 的网络连接。\033[0m"
        before_show_menu
    else
        chmod +x /usr/bin/next-server
        echo -e "\033[32m升级管理脚本成功，请重新运行脚本！\033[0m" && exit 0
    fi
}

# 0: running, 1: not running, 2: not installed
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

check_enabled() {
    temp=$(systemctl is-enabled next-server 2>/dev/null)
    if [[ x"${temp}" == x"enabled" ]]; then
        return 0
    else
        return 1
    fi
}

check_uninstall() {
    check_status
    if [[ $? != 2 ]]; then
        echo ""
        echo -e "\033[33mnext-server 已安装，无需重复安装。\033[0m"
        if [[ $# == 0 ]]; then
            before_show_menu
        fi
        return 1
    else
        return 0
    fi
}

check_install() {
    check_status
    if [[ $? == 2 ]]; then
        echo ""
        echo -e "\033[31m检测到 next-server 尚未安装，请先选择安装！\033[0m"
        if [[ $# == 0 ]]; then
            before_show_menu
        fi
        return 1
    else
        return 0
    fi
}

show_status() {
    check_status
    case $? in
        0)
            echo -e "next-server 状态: \033[32m已运行\033[0m"
            show_enable_status
            ;;
        1)
            echo -e "next-server 状态: \033[31m未运行\033[0m"
            show_enable_status
            ;;
        2)
            echo -e "next-server 状态: \033[33m未安装\033[0m"
            ;;
    esac
}

show_enable_status() {
    check_enabled
    if [[ $? == 0 ]]; then
        echo -e "是否开机自启: \033[32m是\033[0m"
    else
        echo -e "是否开机自启: \033[33m否\033[0m"
    fi
}

show_next_server_version() {
    if [[ -x "$BIN_FILE" ]]; then
        echo -n "next-server 版本: "
        "$BIN_FILE" version
    else
        echo -e "\033[31m未找到可执行程序: ${BIN_FILE}\033[0m"
    fi
    echo ""
    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

show_usage() {
    echo "NeXT-Server 管理脚本使用方法: "
    echo "--------------------------------------------------------"
    echo "  next-server                    - 显示交互管理菜单"
    echo "  next-server start              - 启动 next-server"
    echo "  next-server stop               - 停止 next-server"
    echo "  next-server restart            - 重启 next-server"
    echo "  next-server status             - 查看运行状态"
    echo "  next-server enable             - 设置开机自启"
    echo "  next-server disable            - 取消开机自启"
    echo "  next-server log                - 查看实时运行日志"
    echo "  next-server update             - 更新至最新版本"
    echo "  next-server update [version]   - 更新至指定版本"
    echo "  next-server config             - 修改配置文件"
    echo "  next-server install            - 安装 next-server"
    echo "  next-server uninstall          - 卸载 next-server"
    echo "  next-server version            - 查看版本信息"
    echo "--------------------------------------------------------"
}

show_menu() {
    echo -e "
\033[36m========================================================\033[0m
\033[36m   NeXT-Server 管理脚本 (${version})\033[0m
\033[36m   https://github.com/ShadowRhythm/NeXT-Server\033[0m
\033[36m========================================================\033[0m
  0. 修改配置
————————————————
  1. 安装 next-server
  2. 更新 next-server
  3. 卸载 next-server
————————————————
  4. 启动 next-server
  5. 停止 next-server
  6. 重启 next-server
  7. 查看 next-server 状态
  8. 查看 next-server 实时日志
————————————————
  9. 设置 next-server 开机自启
 10. 取消 next-server 开机自启
————————————————
 11. 查看 next-server 版本
 12. 升级管理脚本
 13. 退出脚本
"
    show_status
    echo && read -p "请输入选项 [0-13]: " num

    case "${num}" in
        0) check_install && config
        ;;
        1) check_uninstall && install
        ;;
        2) check_install && update
        ;;
        3) check_install && uninstall
        ;;
        4) check_install && start
        ;;
        5) check_install && stop
        ;;
        6) check_install && restart
        ;;
        7) check_install && status
        ;;
        8) check_install && show_log
        ;;
        9) check_install && enable
        ;;
        10) check_install && disable
        ;;
        11) check_install && show_next_server_version
        ;;
        12) update_shell
        ;;
        13) exit 0
        ;;
        *) echo -e "\033[31m请输入正确的数字 [0-13]！\033[0m"
        ;;
    esac
}

if [[ $# -gt 0 ]]; then
    case $1 in
        "start") check_install 0 && start 0
        ;;
        "stop") check_install 0 && stop 0
        ;;
        "restart") check_install 0 && restart 0
        ;;
        "status") check_install 0 && status 0
        ;;
        "enable") check_install 0 && enable 0
        ;;
        "disable") check_install 0 && disable 0
        ;;
        "log") check_install 0 && show_log 0
        ;;
        "update") check_install 0 && update 0 "$2"
        ;;
        "config") check_install 0 && config 0
        ;;
        "install") check_uninstall 0 && install 0
        ;;
        "uninstall") check_install 0 && uninstall 0
        ;;
        "version") check_install 0 && show_next_server_version 0
        ;;
        "update_shell") update_shell
        ;;
        *) show_usage
    esac
else
    show_menu
fi

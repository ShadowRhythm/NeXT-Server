#!/bin/bash

cur_dir=$(pwd)

# check root
[[ $EUID -ne 0 ]] && echo -e "必须使用root用户运行此脚本！\n" && exit 1

# check os
if [[ -f /etc/redhat-release ]]; then
    release="centos"
elif cat /etc/issue | grep -Eqi "debian"; then
    release="debian"
elif cat /etc/issue | grep -Eqi "ubuntu"; then
    release="ubuntu"
elif cat /etc/issue | grep -Eqi "centos|red hat|redhat|rocky|alma|fedora"; then
    release="rhel"
elif cat /proc/version | grep -Eqi "debian"; then
    release="debian"
elif cat /proc/version | grep -Eqi "ubuntu"; then
    release="ubuntu"
elif cat /proc/version | grep -Eqi "centos|red hat|redhat|rocky|alma|fedora"; then
    release="rhel"
else
    echo -e "未检测到系统版本！\n" && exit 1
fi

arch=$(arch)

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
    echo "本软件不支持 32 位系统(x86)，请使用 64 位系统(x86_64/arm64/riscv64) 或自行编译"
    exit 2
fi

if [[ $arch == "amd64" ]]; then
    if lscpu | grep -Eqi "avx2"; then
        release="amd64v3"
    fi
fi

echo "架构: ${arch}"

os_version=""

# os version
if [[ -f /etc/os-release ]]; then
    os_version=$(awk -F'[= ."]' '/VERSION_ID/{print $3}' /etc/os-release)
fi
if [[ -z "$os_version" && -f /etc/lsb-release ]]; then
    os_version=$(awk -F'[= ."]+' '/DISTRIB_RELEASE/{print $2}' /etc/lsb-release)
fi

if [[ x"${release}" == x"rhel" ]]; then
    if [[ ${os_version} -lt 8 ]]; then
        echo -e "请使用 CentOS 8 或更高版本的系统！\n" && exit 1
    fi
elif [[ x"${release}" == x"ubuntu" ]]; then
    if [[ ${os_version} -lt 20 ]]; then
        echo -e "请使用 Ubuntu 20 或更高版本的系统！\n" && exit 1
    fi
elif [[ x"${release}" == x"debian" ]]; then
    if [[ ${os_version} -lt 10 ]]; then
        echo -e "请使用 Debian 10 或更高版本的系统！\n" && exit 1
    fi
fi

install_base() {
    if [[ x"${release}" == x"rhel" ]]; then
        dnf install epel-release -y
        dnf install wget curl unzip tar crontabs -y
    else
        apt update -y
        apt install wget curl unzip tar cron -y
    fi
}

# 0: running, 1: not running, 2: not installed
check_status() {
    if [[ ! -f /etc/systemd/system/next-server.service ]]; then
        return 2
    fi

    temp=$(systemctl status next-server | grep Active | awk '{print $3}' | cut -d "(" -f2 | cut -d ")" -f1)

    if [[ x"${temp}" == x"running" ]]; then
        return 0
    else
        return 1
    fi
}

install_acme() {
    curl https://get.acme.sh | sh
}

install_uim_server() {
  if [[ -e /usr/local/next-server/ ]]; then
      rm /usr/local/next-server/ -rf
  fi

  mkdir /usr/local/next-server/ -p
	cd /usr/local/next-server/

  if  [ $# == 0 ] ;then
      last_version=$(curl -Ls "https://api.github.com/repos/SSPanel-NeXT/NeXT-Server/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
      if [[ ! -n "$last_version" ]]; then
          echo -e "检测 next-server 版本失败，可能是超出 Github API 限制，请稍后再试，或手动指定 next-server 版本安装"
          exit 1
      fi
      echo -e "检测到 next-server 最新版本：${last_version}，开始安装"
      wget -q -O /usr/local/next-server/next-server-linux.zip https://github.com/SSPanel-NeXT/NeXT-Server/releases/download/${last_version}/next-server-linux-${arch}.zip
      if [[ $? -ne 0 ]]; then
          echo -e "下载 next-server 失败，请确保你的服务器能够下载 Github 的文件"
          exit 1
      fi
  else
      if [[ $1 == v* ]]; then
          last_version=$1
      else
	        last_version="v"$1
	    fi
      url="https://github.com/SSPanel-NeXT/NeXT-Server/releases/download/${last_version}/next-server-linux-${arch}.zip"
      echo -e "开始安装 next-server ${last_version}"
      wget -q -O /usr/local/next-server/next-server-linux.zip ${url}
      if [[ $? -ne 0 ]]; then
          echo -e "下载 next-server ${last_version} 失败，请确保此版本存在"
          exit 1
      fi
  fi

  unzip next-server-linux.zip
  rm next-server-linux.zip -f
  chmod +x next-server
  mkdir /etc/next-server/ -p
  rm /etc/systemd/system/next-server.service -f
  file="https://github.com/SSPanel-NeXT/mirror/raw/main/next-server/next-server.service"
  wget -q -O /etc/systemd/system/next-server.service ${file}
  systemctl daemon-reload
  systemctl stop next-server
  systemctl enable next-server
  echo -e "next-server ${last_version} 安装完成，已设置开机自启"
  cp geoip.dat /etc/next-server/
  cp geosite.dat /etc/next-server/

  if [[ ! -f /etc/next-server/config.yml ]]; then
      cp config.yml /etc/next-server/
  else
      systemctl start next-server
      sleep 2
      check_status
      echo -e ""
      if [[ $? == 0 ]]; then
          echo -e "next-server 重启成功"
      else
          echo -e "next-server 启动失败"
      fi
  fi

  if [[ ! -f /etc/next-server/dns.json ]]; then
      cp dns.json /etc/next-server/
  fi
  if [[ ! -f /etc/next-server/route.json ]]; then
      cp route.json /etc/next-server/
  fi
  if [[ ! -f /etc/next-server/custom_outbound.json ]]; then
      cp custom_outbound.json /etc/next-server/
  fi
  if [[ ! -f /etc/next-server/custom_inbound.json ]]; then
      cp custom_inbound.json /etc/next-server/
  fi
  if [[ ! -f /etc/next-server/rulelist ]]; then
      cp rulelist /etc/next-server/
  fi

  curl -o /usr/bin/next-server -Ls https://github.com/ShadowRhythm/NeXT-Server/raw/main/release/next-server.sh
  chmod +x /usr/bin/next-server
  cd $cur_dir
  rm -f install.sh
  echo -e ""
  echo "next-server 管理脚本使用方法: "
  echo "------------------------------------------"
  echo "next-server                    - 显示管理菜单 (功能更多)"
  echo "next-server start              - 启动 next-server"
  echo "next-server stop               - 停止 next-server"
  echo "next-server restart            - 重启 next-server"
  echo "next-server status             - 查看 next-server 状态"
  echo "next-server enable             - 设置 next-server 开机自启"
  echo "next-server disable            - 取消 next-server 开机自启"
  echo "next-server log                - 查看 next-server 日志"
  echo "next-server update             - 更新 next-server"
  echo "next-server update x.x.x       - 更新 next-server 指定版本"
  echo "next-server config             - 显示配置文件内容"
  echo "next-server install            - 安装 next-server"
  echo "next-server uninstall          - 卸载 next-server"
  echo "next-server version            - 查看 next-server 版本"
  echo "------------------------------------------"
}

echo -e "开始安装"
install_base
# install_acme
install_uim_server $1

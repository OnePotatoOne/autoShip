#!/usr/bin/env bash
# ==============================================================================
# autoShip 产品服务器更新 Agent 一键安装与权限加固脚本（对应设计文档 9.1 & 15 节）
# 编码格式：UTF-8
# 职责说明：
#   1. 创建无登录权限的专用低权限系统账号 autoship-agent；
#   2. 初始化配置目录、暂存目录、root 专属 0700 隔离目录及数据库备份目录权限；
#   3. 安装 autoship-agent（常驻进程）与 autoship-apply（root 提权小工具）二进制；
#   4. 写入无任何通配参数的 /etc/sudoers.d/autoship 并通过 visudo 语法核验；
#   5. 安装并启用 autoship-agent.service systemd 守护单元。
# ==============================================================================

set -euo pipefail

readonly AGENT_USER="autoship-agent"
readonly AGENT_GROUP="autoship-agent"

readonly INSTALL_AGENT_BIN="/usr/local/bin/autoship-agent"
readonly INSTALL_APPLY_BIN="/usr/local/sbin/autoship-apply"
readonly ETC_DIR="/etc/autoship"
readonly AGENT_STATE_DIR="/var/lib/autoship-agent"
readonly AGENT_STAGING_DIR="/var/lib/autoship-agent/staging/current"
readonly APPLY_STATE_DIR="/var/lib/autoship-apply"
readonly APPLY_WORK_DIR="/var/lib/autoship-apply/work"
readonly HOOK_DIR="/usr/local/libexec/autoship"
readonly INSTALL_HOOK_BIN="/usr/local/libexec/autoship/deploy-hook"
readonly BACKUP_ROOT_DIR="/var/backups/autoship"
readonly SUDOERS_FILE="/etc/sudoers.d/autoship"
readonly SYSTEMD_UNIT_FILE="/etc/systemd/system/autoship-agent.service"

SRC_AGENT_BIN="./autoship-agent"
SRC_APPLY_BIN="./autoship-apply"
SRC_CONFIG_FILE=""
SRC_SERVER_PUB=""
SRC_RELEASE_PUB=""
SRC_HOOK_BIN=""

# usage 打印安装脚本的命令行参数说明与调用示例。
# 参数：无
# 返回值：始终返回 0
# 调用方式：usage
usage() {
  cat <<'EOF'
用法: sudo ./install-update-agent.sh [选项]

选项:
  --agent-bin <路径>     指定待安装的 autoship-agent 二进制路径（默认 ./autoship-agent）
  --apply-bin <路径>     指定待安装的 autoship-apply 二进制路径（默认 ./autoship-apply）
  --config <路径>        可选：待复制到 /etc/autoship/update-agent.yaml 的初始配置文件
  --server-pub <路径>    可选：待安装到 /etc/autoship/server.pub 的 Server 票据验签公钥文件
  --release-pub <路径>   可选：待安装到 /etc/autoship/release.pub 的发布清单验签公钥文件
  --hook-bin <路径>      可选：初始安装到 /usr/local/libexec/autoship/deploy-hook 的产品部署钩子
  -h, --help             显示本帮助信息并退出
EOF
}

# require_root 校验当前执行用户是否为 root（UID=0），非 root 立即终止退出。
# 参数：无
# 返回值：root 返回 0；非 root 输出错误并以状态码 1 退出
# 调用方式：require_root
require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    echo "[ERROR] 请使用 root 权限运行本安装脚本（例如 sudo ./install-update-agent.sh）" >&2
    exit 1
  fi
}

# parse_args 解析命令行传入的二进制、配置文件与公钥路径参数。
# 参数：$@ 为脚本接收到的全部命令行参数
# 返回值：解析成功返回 0；遇到未知参数以状态码 2 退出
# 调用方式：parse_args "$@"
parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --agent-bin)
        SRC_AGENT_BIN="${2:?缺少 --agent-bin 参数值}"
        shift 2
        ;;
      --apply-bin)
        SRC_APPLY_BIN="${2:?缺少 --apply-bin 参数值}"
        shift 2
        ;;
      --config)
        SRC_CONFIG_FILE="${2:?缺少 --config 参数值}"
        shift 2
        ;;
      --server-pub)
        SRC_SERVER_PUB="${2:?缺少 --server-pub 参数值}"
        shift 2
        ;;
      --release-pub)
        SRC_RELEASE_PUB="${2:?缺少 --release-pub 参数值}"
        shift 2
        ;;
      --hook-bin)
        SRC_HOOK_BIN="${2:?缺少 --hook-bin 参数值}"
        shift 2
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        echo "[ERROR] 未知参数: $1" >&2
        usage >&2
        exit 2
        ;;
    esac
  done
}

# ensure_system_user 创建无交互 Shell 的专用系统用户与用户组 autoship-agent（已存在则幂等跳过）。
# 参数：无
# 返回值：成功返回 0，失败返回 useradd/groupadd 非零退出码
# 调用方式：ensure_system_user
ensure_system_user() {
  if ! getent group "${AGENT_GROUP}" >/dev/null 2>&1; then
    groupadd --system "${AGENT_GROUP}"
  fi

  if ! id -u "${AGENT_USER}" >/dev/null 2>&1; then
    local nologin_shell="/usr/sbin/nologin"
    if [ ! -x "${nologin_shell}" ] && [ -x "/sbin/nologin" ]; then
      nologin_shell="/sbin/nologin"
    elif [ ! -x "${nologin_shell}" ]; then
      nologin_shell="/bin/false"
    fi
    useradd \
      --system \
      --gid "${AGENT_GROUP}" \
      --no-create-home \
      --home-dir "${AGENT_STATE_DIR}" \
      --shell "${nologin_shell}" \
      --comment "autoShip Update Agent Unprivileged Daemon" \
      "${AGENT_USER}"
  fi
}

# init_directories_and_keys 创建配置、暂存、root 隔离工作区、钩子与数据库备份目录并严格收敛属主和权限位。
# 参数：无
# 返回值：成功返回 0，失败返回非零退出码
# 调用方式：init_directories_and_keys
init_directories_and_keys() {
  # 1. /etc/autoship 由 root 持有，公钥仅 root 可写（其他账号只读）
  install -d -o root -g root -m 0755 "${ETC_DIR}"

  if [ -n "${SRC_SERVER_PUB}" ]; then
    install -o root -g root -m 0644 "${SRC_SERVER_PUB}" "${ETC_DIR}/server.pub"
  fi
  if [ -n "${SRC_RELEASE_PUB}" ]; then
    install -o root -g root -m 0644 "${SRC_RELEASE_PUB}" "${ETC_DIR}/release.pub"
  fi
  if [ -n "${SRC_CONFIG_FILE}" ]; then
    # 配置文件包含首次注册一次性令牌，限制为 root:autoship-agent 0640
    install -o root -g "${AGENT_GROUP}" -m 0640 "${SRC_CONFIG_FILE}" "${ETC_DIR}/update-agent.yaml"
  fi

  # 2. autoship-agent 状态与暂存目录：归专用低权限账号所有，0755 以便 root autoship-apply 复制暂存包
  install -d -o "${AGENT_USER}" -g "${AGENT_GROUP}" -m 0755 "${AGENT_STATE_DIR}"
  install -d -o "${AGENT_USER}" -g "${AGENT_GROUP}" -m 0755 "${AGENT_STATE_DIR}/staging"
  install -d -o "${AGENT_USER}" -g "${AGENT_GROUP}" -m 0755 "${AGENT_STAGING_DIR}"

  # 3. autoship-apply 专属隔离工作区与账本目录：严格限制为 root:root 0700，彻底隔离常驻账号
  install -d -o root -g root -m 0700 "${APPLY_STATE_DIR}"
  install -d -o root -g root -m 0700 "${APPLY_WORK_DIR}"

  # 4. 产品部署钩子目录与初始钩子（root:root 0700）
  install -d -o root -g root -m 0755 "${HOOK_DIR}"
  if [ -n "${SRC_HOOK_BIN}" ]; then
    install -o root -g root -m 0700 "${SRC_HOOK_BIN}" "${INSTALL_HOOK_BIN}"
  fi

  # 5. 数据库备份专用根目录（单次部署子目录将由 autoship-apply 以 0700 权限创建）
  install -d -o root -g root -m 0755 "${BACKUP_ROOT_DIR}"
}

# install_binaries 校验并原子安装 autoship-agent 与 autoship-apply 可执行文件（均归 root 所有，防止篡改）。
# 参数：无
# 返回值：成功返回 0；源文件缺失以状态码 1 退出
# 调用方式：install_binaries
install_binaries() {
  if [ ! -f "${SRC_AGENT_BIN}" ]; then
    echo "[ERROR] 未找到 autoship-agent 二进制文件: ${SRC_AGENT_BIN}" >&2
    exit 1
  fi
  if [ ! -f "${SRC_APPLY_BIN}" ]; then
    echo "[ERROR] 未找到 autoship-apply 二进制文件: ${SRC_APPLY_BIN}" >&2
    exit 1
  fi

  install -o root -g root -m 0755 "${SRC_AGENT_BIN}" "${INSTALL_AGENT_BIN}"
  install -o root -g root -m 0755 "${SRC_APPLY_BIN}" "${INSTALL_APPLY_BIN}"
}

# install_sudoers_rule 生成零通配符的受限 sudoers 规则文件，并通过 visudo 语法校验后原子生效（9.1 节）。
# 参数：无
# 返回值：成功返回 0；visudo 校验不通过返回非零退出码
# 调用方式：install_sudoers_rule
install_sudoers_rule() {
  install -d -o root -g root -m 0750 /etc/sudoers.d
  local tmp_sudoers="${SUDOERS_FILE}.tmp.$$"

  cat > "${tmp_sudoers}" <<EOF
# autoShip 受限提权规则（自动生成，严禁添加任何 * 通配符参数）
Defaults:${AGENT_USER} env_reset,!setenv,secure_path="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
${AGENT_USER} ALL=(root) NOPASSWD: ${INSTALL_APPLY_BIN} upgrade
${AGENT_USER} ALL=(root) NOPASSWD: ${INSTALL_APPLY_BIN} rollback
${AGENT_USER} ALL=(root) NOPASSWD: ${INSTALL_APPLY_BIN} backup
${AGENT_USER} ALL=(root) NOPASSWD: ${INSTALL_APPLY_BIN} logs
${AGENT_USER} ALL=(root) NOPASSWD: ${INSTALL_APPLY_BIN} cleanup
EOF

  chown root:root "${tmp_sudoers}"
  chmod 0440 "${tmp_sudoers}"

  if command -v visudo >/dev/null 2>&1; then
    if ! visudo -cf "${tmp_sudoers}" >/dev/null; then
      rm -f "${tmp_sudoers}"
      echo "[ERROR] 生成的 sudoers 规则未通过 visudo 语法检查" >&2
      exit 1
    fi
  fi

  mv -f "${tmp_sudoers}" "${SUDOERS_FILE}"
}

# install_systemd_unit 写入 autoship-agent.service 单元配置并执行 daemon-reload 与开机自启注册。
# 参数：无
# 返回值：成功返回 0
# 调用方式：install_systemd_unit
install_systemd_unit() {
  cat > "${SYSTEMD_UNIT_FILE}" <<EOF
[Unit]
Description=autoShip Product Server Update Agent
Documentation=https://autoship.internal/docs
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${AGENT_USER}
Group=${AGENT_GROUP}
WorkingDirectory=${AGENT_STATE_DIR}
ExecStart=${INSTALL_AGENT_BIN} -config ${ETC_DIR}/update-agent.yaml
Restart=always
RestartSec=5s
# 注意：必须保持 NoNewPrivileges=false，以便受限 sudoers 放行固定 autoship-apply 子命令提权
NoNewPrivileges=false
ProtectHome=true
PrivateTmp=true
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF

  chown root:root "${SYSTEMD_UNIT_FILE}"
  chmod 0644 "${SYSTEMD_UNIT_FILE}"

  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload
    systemctl enable autoship-agent.service
  fi
}

# main 按固定顺序执行权限检查、系统用户初始化、目录授权、二进制部署、sudoers 配置与 systemd 注册。
# 参数：$@ 为脚本全部命令行参数
# 返回值：全部成功返回 0
# 调用方式：main "$@"
main() {
  require_root
  parse_args "$@"
  ensure_system_user
  init_directories_and_keys
  install_binaries
  install_sudoers_rule
  install_systemd_unit

  echo "[OK] autoShip 更新 Agent 安装完成。"
  echo "  - 常驻进程: ${INSTALL_AGENT_BIN} (运行账号: ${AGENT_USER})"
  echo "  - 提权工具: ${INSTALL_APPLY_BIN} (sudo 规则: ${SUDOERS_FILE})"
  echo "  - 配置文件: ${ETC_DIR}/update-agent.yaml"
  echo "请核对 ${ETC_DIR}/server.pub、${ETC_DIR}/release.pub 与 update-agent.yaml 后执行: systemctl start autoship-agent"
}

main "$@"

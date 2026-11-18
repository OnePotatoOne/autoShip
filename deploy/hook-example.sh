#!/usr/bin/env bash
# ==============================================================================
# autoShip 产品部署钩子参考实现（完整遵循设计文档第 6 节与附录 A 契约）
# 编码格式：UTF-8
# 核心安全与幂等保证：
#   1. 安装位置等全部路径均为脚本内置常量，绝不从环境变量读取；
#   2. 对候选新程序执行的任何预检均降权以业务服务账号（SERVICE_USER）执行，绝不以 root 运行；
#   3. install 按 deployment_id 独立保存升级前版本备份，启动失败自动恢复旧版并返回非零；
#   4. rollback --deployment <ID> 严格按部署 ID 定位备份，绝不把换下的坏版本覆盖回备份，支持无限次幂等重试；
#   5. backup-db 使用产品服务器本地 root 持有的数据库凭据导出压缩备份并校验完整性；
#   6. 支持 status、logs <N>、cleanup 固定契约子命令。
# ==============================================================================

set -euo pipefail

# 固定产品部署常量（严禁从环境变量读取安装路径）
readonly SERVICE_NAME="example-product"
readonly SERVICE_USER="example-product"
readonly INSTALL_DIR="/opt/example-product/bin"
readonly INSTALL_BIN="/opt/example-product/bin/app"
readonly BACKUP_STORE_DIR="/var/lib/example-product/autoship-backups"
readonly DB_CONF_FILE="/etc/example-product/db.conf"
readonly APP_LOG_FILE="/var/log/example-product/app.log"
readonly KEEP_BINARY_BACKUPS=5

# log_info 向标准错误流输出带时间戳的提示信息（避免污染可能被上层读取的干净标准输出）。
# 参数：$* 为日志消息内容
# 返回值：始终返回 0
# 调用方式：log_info "开始安装..."
log_info() {
  printf '[hook][INFO] %s\n' "$*" >&2
}

# log_err 向标准错误流输出带时间戳的错误消息。
# 参数：$* 为错误描述文本
# 返回值：始终返回 0
# 调用方式：log_err "预检失败"
log_err() {
  printf '[hook][ERROR] %s\n' "$*" >&2
}

# validate_safe_id 校验部署 ID 仅由安全 ASCII 字符组成，阻断目录穿越或命令注入。
# 参数：$1 为待校验的 deployment_id 字符串
# 返回值：合法返回 0；非法输出错误并以状态码 2 退出
# 调用方式：validate_safe_id "$deployment_id"
validate_safe_id() {
  local id="${1:-}"
  if [ -z "${id}" ] || [ "${#id}" -gt 64 ]; then
    log_err "部署 ID 长度非法: '${id}'"
    exit 2
  fi
  case "${id}" in
    *[!a-zA-Z0-9._-]*)
      log_err "部署 ID 包含非法字符: '${id}'"
      exit 2
      ;;
  esac
}

# sha256_file 计算指定文件的 64 位小写十六进制 SHA-256 哈希（兼容 Linux sha256sum 与 BSD/macOS shasum）。
# 参数：$1 为目标文件路径
# 返回值：标准输出打印 64 字符哈希串，成功返回 0
# 调用方式：hash_val="$(sha256_file "/path/to/file")"
sha256_file() {
  local target="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${target}" | awk '{print $1}'
  else
    shasum -a 256 "${target}" | awk '{print $1}'
  fi
}

# run_candidate_precheck_unprivileged 将候选程序复制到临时隔离文件并降权以业务服务账号执行预检（严禁以 root 运行候选程序）。
# 参数：$1 为候选程序二进制路径
# 返回值：预检通过返回 0；预检失败清理临时文件并返回非零退出码
# 调用方式：run_candidate_precheck_unprivileged "$candidate_bin"
run_candidate_precheck_unprivileged() {
  local candidate_bin="$1"
  local tmp_check_bin
  tmp_check_bin="$(mktemp /tmp/autoship-cand-check.XXXXXX)"
  cp "${candidate_bin}" "${tmp_check_bin}"
  chmod 0755 "${tmp_check_bin}"

  local rc=0
  if command -v runuser >/dev/null 2>&1; then
    # Linux 标准降权执行：强制切换到低权限业务服务账号 SERVICE_USER，不以 root 执行候选代码
    runuser -u "${SERVICE_USER}" -- "${tmp_check_bin}" --version >/dev/null || rc=$?
  elif command -v su >/dev/null 2>&1; then
    su -s /bin/sh "${SERVICE_USER}" -c "'${tmp_check_bin}' --version" >/dev/null || rc=$?
  else
    rm -f "${tmp_check_bin}"
    log_err "系统缺少 runuser/su 工具，拒绝以 root 身份执行候选程序预检"
    return 1
  fi

  rm -f "${tmp_check_bin}"
  if [ "${rc}" -ne 0 ]; then
    log_err "候选程序在低权限服务账号 (${SERVICE_USER}) 下预检失败，退出码=${rc}"
    return "${rc}"
  fi
  return 0
}

# restart_and_verify_service 重启 systemd 产品服务并确认进入 active 运行态。
# 参数：无
# 返回值：服务正常启动返回 0；启动失败返回非零状态码
# 调用方式：restart_and_verify_service
restart_and_verify_service() {
  systemctl restart "${SERVICE_NAME}"
  sleep 1
  if ! systemctl is-active --quiet "${SERVICE_NAME}"; then
    return 1
  fi
  return 0
}

# cmd_install 执行候选版本降权预检、按部署 ID 归档旧版本、原子替换安装及启动验证（失败自动恢复旧版并返回非零）。
# 参数：$1 为候选二进制路径，$2 固定为 "--deployment"，$3 为部署 ID
# 返回值：升级并启动成功返回 0；失败自动还原旧版并以非零退出码退出
# 调用方式：cmd_install "/path/to/new-app" "--deployment" "dep-001"
cmd_install() {
  if [ "$#" -ne 3 ] || [ "$2" != "--deployment" ]; then
    log_err "用法: install <制品路径> --deployment <部署ID>"
    exit 2
  fi
  local candidate_bin="$1"
  local deployment_id="$3"
  validate_safe_id "${deployment_id}"

  if [ ! -f "${candidate_bin}" ]; then
    log_err "候选制品文件不存在: ${candidate_bin}"
    exit 3
  fi

  # 1. 降权以业务服务账号执行候选二进制预检（绝不以 root 运行候选二进制）
  run_candidate_precheck_unprivileged "${candidate_bin}"

  install -d -o root -g root -m 0700 "${BACKUP_STORE_DIR}"
  install -d -o root -g root -m 0755 "${INSTALL_DIR}"

  local meta_file="${BACKUP_STORE_DIR}/${deployment_id}.meta"
  local bak_file="${BACKUP_STORE_DIR}/${deployment_id}.bin"

  # 2. 记录“部署 ID -> 升级前版本备份”（若同一部署 ID 重试，绝不覆盖首次保存的升级前基线）
  if [ ! -f "${meta_file}" ]; then
    if [ -f "${INSTALL_BIN}" ]; then
      cp -p "${INSTALL_BIN}" "${bak_file}"
      chmod 0700 "${bak_file}"
      local prev_sha
      prev_sha="$(sha256_file "${bak_file}")"
      printf 'had_previous=1\nprev_sha256=%s\ncreated_at=%s\n' "${prev_sha}" "$(date +%s)" > "${meta_file}"
      chmod 0600 "${meta_file}"
    else
      printf 'had_previous=0\ncreated_at=%s\n' "$(date +%s)" > "${meta_file}"
      chmod 0600 "${meta_file}"
    fi
  fi

  # 3. 原子替换目标二进制
  local tmp_install="${INSTALL_BIN}.tmp.$$"
  install -o root -g root -m 0755 "${candidate_bin}" "${tmp_install}"
  mv -f "${tmp_install}" "${INSTALL_BIN}"

  # 4. 启动新版本；若启动失败，立即自动恢复该部署 ID 对应的旧版本并返回非零退出码
  if ! restart_and_verify_service; then
    log_err "新版本服务启动失败，正在自动还原部署 ${deployment_id} 之前的版本..."
    if grep -q '^had_previous=1$' "${meta_file}" && [ -f "${bak_file}" ]; then
      local tmp_restore="${INSTALL_BIN}.restore.$$"
      install -o root -g root -m 0755 "${bak_file}" "${tmp_restore}"
      mv -f "${tmp_restore}" "${INSTALL_BIN}"
      systemctl restart "${SERVICE_NAME}" || true
    else
      systemctl stop "${SERVICE_NAME}" || true
      rm -f "${INSTALL_BIN}"
    fi
    exit 20
  fi

  log_info "部署 ${deployment_id} 安装并启动成功"
}

# cmd_rollback 按部署 ID 幂等还原至该次部署之前的版本（重复执行结果不变；已是目标版本时零中断返回）。
# 参数：$1 固定为 "--deployment"，$2 为部署 ID
# 返回值：成功回到目标基线返回 0；备份缺失或恢复失败返回非零退出码
# 调用方式：cmd_rollback "--deployment" "dep-001"
cmd_rollback() {
  if [ "$#" -ne 2 ] || [ "$1" != "--deployment" ]; then
    log_err "用法: rollback --deployment <部署ID>"
    exit 2
  fi
  local deployment_id="$2"
  validate_safe_id "${deployment_id}"

  local meta_file="${BACKUP_STORE_DIR}/${deployment_id}.meta"
  local bak_file="${BACKUP_STORE_DIR}/${deployment_id}.bin"

  if [ ! -f "${meta_file}" ]; then
    log_err "未找到部署 ID=${deployment_id} 的备份元数据，拒绝盲目回滚"
    exit 15
  fi

  # 若该次部署为首次全新安装（之前无旧版本），幂等停止并移除程序
  if grep -q '^had_previous=0$' "${meta_file}"; then
    systemctl stop "${SERVICE_NAME}" || true
    rm -f "${INSTALL_BIN}"
    log_info "部署 ${deployment_id} 之前无历史版本，已幂等清理"
    return 0
  fi

  if [ ! -f "${bak_file}" ]; then
    log_err "部署 ID=${deployment_id} 的备份二进制文件缺失: ${bak_file}"
    exit 16
  fi

  local target_sha
  target_sha="$(sha256_file "${bak_file}")"

  # 幂等优化：若当前线上二进制哈希已等于目标备份且服务正在运行，什么都不做直接返回 0
  if [ -f "${INSTALL_BIN}" ]; then
    local current_sha
    current_sha="$(sha256_file "${INSTALL_BIN}")"
    if [ "${current_sha}" = "${target_sha}" ] && systemctl is-active --quiet "${SERVICE_NAME}"; then
      log_info "当前运行版本已与部署 ${deployment_id} 前的备份完全一致，无需重复重启"
      return 0
    fi
  fi

  # 原子还原旧版本（严禁将换下的坏版本写回 bak_file，保证二次回滚结果恒定）
  local tmp_restore="${INSTALL_BIN}.rollback.$$"
  install -o root -g root -m 0755 "${bak_file}" "${tmp_restore}"
  mv -f "${tmp_restore}" "${INSTALL_BIN}"

  if ! restart_and_verify_service; then
    log_err "回滚至部署 ${deployment_id} 之前的版本后服务启动失败"
    exit 21
  fi
  log_info "已按部署 ID=${deployment_id} 成功还原旧版本"
}

# cmd_backup_db 使用产品服务器本地 root 持有的数据库配置将业务库导出至指定专用目录（9.5 节）。
# 参数：$1="--deployment"，$2=部署ID，$3="--dir"，$4=备份目录绝对路径
# 返回值：导出并校验通过返回 0；凭据缺失或导出失败返回非零退出码
# 调用方式：cmd_backup_db "--deployment" "dep-001" "--dir" "/var/backups/autoship/prod/dep-001"
cmd_backup_db() {
  if [ "$#" -ne 4 ] || [ "$1" != "--deployment" ] || [ "$3" != "--dir" ]; then
    log_err "用法: backup-db --deployment <部署ID> --dir <备份目录>"
    exit 2
  fi
  local deployment_id="$2"
  local backup_dir="$4"
  validate_safe_id "${deployment_id}"

  case "${backup_dir}" in
    /*) ;;
    *)
      log_err "备份目录必须为绝对路径: ${backup_dir}"
      exit 2
      ;;
  esac
  if [ ! -d "${backup_dir}" ]; then
    log_err "目标备份目录不存在: ${backup_dir}"
    exit 3
  fi

  if [ ! -f "${DB_CONF_FILE}" ]; then
    log_err "缺少本地数据库备份凭据文件 ${DB_CONF_FILE}"
    exit 18
  fi
  if ! command -v mysqldump >/dev/null 2>&1; then
    log_err "当前服务器未安装 mysqldump，无法执行数据库备份"
    exit 19
  fi

  # 从 /etc/example-product/db.conf 读取 DB_NAME（[client] 段供 mysqldump --defaults-extra-file 直接读取密码）
  local db_name
  db_name="$(awk -F= '/^[[:space:]]*database[[:space:]]*=/ {gsub(/[[:space:]]/, "", $2); print $2; exit}' "${DB_CONF_FILE}")"
  if [ -z "${db_name}" ]; then
    db_name="${SERVICE_NAME}"
  fi

  local dump_file="${backup_dir}/${db_name}-${deployment_id}.sql.gz"
  mysqldump \
    --defaults-extra-file="${DB_CONF_FILE}" \
    --single-transaction \
    --quick \
    --routines \
    --triggers \
    "${db_name}" | gzip -9 > "${dump_file}"

  chmod 0600 "${dump_file}"
  if [ ! -s "${dump_file}" ] || ! gzip -t "${dump_file}"; then
    rm -f "${dump_file}"
    log_err "导出的数据库压缩文件为空或校验损坏"
    exit 22
  fi

  local size_bytes dump_sha
  size_bytes="$(wc -c < "${dump_file}" | tr -d ' ')"
  dump_sha="$(sha256_file "${dump_file}")"
  printf '%s\t%s\t%s\n' "$(basename "${dump_file}")" "${size_bytes}" "${dump_sha}"
}

# cmd_status 输出产品 systemd 服务的当前运行状态。
# 参数：无
# 返回值：运行中返回 0，未运行返回 systemctl 非零状态码
# 调用方式：cmd_status
cmd_status() {
  systemctl status "${SERVICE_NAME}" --no-pager
}

# cmd_logs 输出产品服务最近 N 行运行日志（优先读取 journalctl，回退读取本地日志文件）。
# 参数：$1 为日志行数（可选，默认 200，必须为正整数）
# 返回值：成功返回 0
# 调用方式：cmd_logs 200
cmd_logs() {
  local lines="${1:-200}"
  case "${lines}" in
    ''|*[!0-9]*)
      log_err "日志行数必须为正整数: ${lines}"
      exit 2
      ;;
  esac

  if command -v journalctl >/dev/null 2>&1; then
    journalctl -u "${SERVICE_NAME}" -n "${lines}" --no-pager
  elif [ -f "${APP_LOG_FILE}" ]; then
    tail -n "${lines}" "${APP_LOG_FILE}"
  else
    printf 'no logs available for %s\n' "${SERVICE_NAME}"
  fi
}

# cmd_cleanup 保留最近 KEEP_BINARY_BACKUPS 份部署前程序备份，清理更早的历史程序备份文件。
# 参数：无
# 返回值：成功返回 0
# 调用方式：cmd_cleanup
cmd_cleanup() {
  if [ ! -d "${BACKUP_STORE_DIR}" ]; then
    return 0
  fi

  # 按 .meta 文件修改时间倒序排列，保留最近 KEEP_BINARY_BACKUPS 份
  local idx=0
  local meta_path base_id
  for meta_path in $(ls -1t "${BACKUP_STORE_DIR}"/*.meta 2>/dev/null || true); do
    idx=$((idx + 1))
    if [ "${idx}" -le "${KEEP_BINARY_BACKUPS}" ]; then
      continue
    fi
    base_id="${meta_path%.meta}"
    rm -f "${meta_path}" "${base_id}.bin"
    log_info "已清理过期程序备份: $(basename "${base_id}")"
  done
}

# main 解析第一个子命令参数并分发至对应契约处理函数。
# 参数：$@ 为钩子收到的全部命令行参数
# 返回值：取决于对应子命令退出码
# 调用方式：main "$@"
main() {
  local subcmd="${1:-}"
  if [ -z "${subcmd}" ]; then
    log_err "缺少子命令（支持: install | rollback | backup-db | status | logs | cleanup）"
    exit 2
  fi
  shift

  case "${subcmd}" in
    install)
      cmd_install "$@"
      ;;
    rollback)
      cmd_rollback "$@"
      ;;
    backup-db)
      cmd_backup_db "$@"
      ;;
    status)
      cmd_status "$@"
      ;;
    logs)
      cmd_logs "$@"
      ;;
    cleanup)
      cmd_cleanup "$@"
      ;;
    *)
      log_err "不支持的钩子子命令: ${subcmd}"
      exit 2
      ;;
  esac
}

main "$@"

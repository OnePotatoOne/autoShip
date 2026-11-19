/**
 * autoShip 管理后台通用状态标签映射、数据归一化与格式化工具函数集（UTF-8）。
 */

/**
 * 标签展示元数据（中文名称与 ng-zorro 预设颜色）。
 */
export interface BadgeMeta {
  label: string;
  color: string;
}

/**
 * 将需求状态编码映射为中文状态名与徽标颜色（对应设计文档 4.1 节）。
 * @param status 后端返回的需求状态字符串
 * @returns 包含中文文案与颜色的 BadgeMeta；通过模板直接调用展示
 */
export function getRequirementStatusMeta(status?: string): BadgeMeta {
  switch ((status || '').toLowerCase()) {
    case 'clarifying':
      return { label: '澄清中', color: 'processing' };
    case 'pending_ai':
    case 'awaiting_ai':
      return { label: '待 AI 跟进', color: 'warning' };
    case 'submitted':
      return { label: '已提交', color: 'cyan' };
    case 'triaged':
      return { label: '已分拣', color: 'geekblue' };
    case 'backlog':
    case 'scheduled':
    case 'ready':
      return { label: '待排期', color: 'blue' };
    case 'in_progress':
      return { label: '进行中', color: 'purple' };
    case 'online':
    case 'released':
    case 'deployed':
      return { label: '已上线', color: 'success' };
    case 'closed':
      return { label: '已关闭', color: 'default' };
    default:
      return { label: status || '未知状态', color: 'default' };
  }
}

/**
 * 将需求问题类型编码映射为中文名称与徽标颜色。
 * @param issueType 问题类型标识（bug / feature / improvement / inquiry）
 * @returns BadgeMeta 对象；供需求表格与抽屉调用
 */
export function getIssueTypeMeta(issueType?: string): BadgeMeta {
  switch ((issueType || '').toLowerCase()) {
    case 'bug':
    case 'defect':
      return { label: '缺陷修复', color: 'red' };
    case 'feature':
      return { label: '新增功能', color: 'blue' };
    case 'improvement':
    case 'enhancement':
      return { label: '体验改进', color: 'cyan' };
    case 'inquiry':
    case 'question':
      return { label: '使用咨询', color: 'gold' };
    default:
      return { label: issueType || '未分类', color: 'default' };
  }
}

/**
 * 将优先级标识映射为中文文案与颜色。
 * @param priority 优先级字符串（P0-P3 或 urgent/high/medium/low）
 * @returns BadgeMeta 对象；供需求池和开发任务展示调用
 */
export function getPriorityMeta(priority?: string): BadgeMeta {
  switch ((priority || '').toUpperCase()) {
    case 'P0':
    case 'URGENT':
    case 'CRITICAL':
      return { label: 'P0 紧急', color: 'magenta' };
    case 'P1':
    case 'HIGH':
      return { label: 'P1 高优', color: 'red' };
    case 'P2':
    case 'MEDIUM':
    case 'NORMAL':
      return { label: 'P2 常规', color: 'blue' };
    case 'P3':
    case 'LOW':
      return { label: 'P3 低优', color: 'default' };
    default:
      return { label: priority || 'P2 常规', color: 'blue' };
  }
}

/**
 * 将开发任务状态编码映射为中文状态名与徽标颜色（对应设计文档 4.1 节）。
 * @param status 开发任务状态编码
 * @returns BadgeMeta 对象；供开发任务列表、详情及需求三态卡片调用
 */
export function getWorkStatusMeta(status?: string): BadgeMeta {
  switch ((status || '').toLowerCase()) {
    case 'preparing':
    case 'queued':
      return { label: '准备中', color: 'default' };
    case 'awaiting_plan':
    case 'pending_plan':
      return { label: '待确认方案', color: 'gold' };
    case 'coding':
    case 'developing':
    case 'in_progress':
    case 'validating':
      return { label: '开发中', color: 'processing' };
    case 'awaiting_answer':
    case 'ask_owner':
      return { label: '等待答复', color: 'warning' };
    case 'awaiting_acceptance':
    case 'pending_acceptance':
      return { label: '待验收', color: 'purple' };
    case 'accepted':
    case 'approved':
    case 'completed':
      return { label: '已通过', color: 'success' };
    case 'paused':
      return { label: '已暂停', color: 'orange' };
    case 'abandoned':
      return { label: '已放弃', color: 'default' };
    default:
      return { label: status || '未启动', color: 'default' };
  }
}

/**
 * 将版本状态编码映射为中文状态名与徽标颜色（对应设计文档 4.1 节）。
 * @param status 版本状态编码
 * @returns BadgeMeta 对象；供版本列表、部署弹窗及需求详情调用
 */
export function getReleaseStatusMeta(status?: string): BadgeMeta {
  switch ((status || '').toLowerCase()) {
    case 'publishing':
    case 'building':
      return { label: '发布中', color: 'processing' };
    case 'publish_failed':
    case 'failed':
      return { label: '发布失败', color: 'error' };
    case 'pending_deploy':
    case 'ready_to_deploy':
      return { label: '待部署', color: 'gold' };
    case 'deployed':
      return { label: '已部署', color: 'success' };
    case 'superseded':
      return { label: '已取代', color: 'default' };
    default:
      return { label: status || '尚未发版', color: 'default' };
  }
}

/**
 * 将生产部署状态编码映射为中文状态名与徽标颜色（对应设计文档 4.1 & 9.4 节）。
 * @param status 部署状态编码
 * @returns BadgeMeta 对象；供部署中心与总览看板调用
 */
export function getDeploymentStatusMeta(status?: string): BadgeMeta {
  switch ((status || '').toLowerCase()) {
    case 'pending':
    case 'pending_confirm':
      return { label: '待确认', color: 'gold' };
    case 'backing_up':
      return { label: '备份中', color: 'cyan' };
    case 'deploying':
    case 'running':
      return { label: '部署中', color: 'processing' };
    case 'success':
    case 'succeeded':
      return { label: '成功', color: 'success' };
    case 'failed_rolled_back':
    case 'rolled_back':
      return { label: '失败已回滚', color: 'warning' };
    case 'rollback_failed':
      return { label: '回滚失败（紧急）', color: 'error' };
    default:
      return { label: status || '尚未部署', color: 'default' };
  }
}

/**
 * 将数据库迁移风险等级编码映射为中文说明与颜色（对应设计文档 8.7 & 9.5 节）。
 * @param risk 迁移风险等级编码
 * @returns BadgeMeta 对象；供验收弹窗与确认部署弹窗展示调用
 */
export function getMigrationRiskMeta(risk?: string): BadgeMeta {
  switch ((risk || '').toLowerCase()) {
    case 'none':
    case '':
      return { label: '无表结构变更', color: 'green' };
    case 'additive':
    case 'safe':
      return { label: '仅新增表/字段', color: 'blue' };
    case 'data_rewrite':
    case 'rewrite':
      return { label: '含数据改写（需备份）', color: 'orange' };
    case 'destructive':
      return { label: '含破坏性变更（需特批）', color: 'red' };
    default:
      return { label: risk || '无表结构变更', color: 'default' };
  }
}

/**
 * 将门禁或步骤状态映射为徽标展示元数据。
 * @param status 门禁或部署步骤状态字符串
 * @returns BadgeMeta 对象；供门禁表与部署分步日志调用
 */
export function getCheckStatusMeta(status?: string): BadgeMeta {
  switch ((status || '').toLowerCase()) {
    case 'passed':
    case 'success':
    case 'succeeded':
    case 'ok':
      return { label: '通过', color: 'success' };
    case 'failed':
    case 'error':
    case 'blocked':
      return { label: '未通过', color: 'error' };
    case 'running':
    case 'in_progress':
      return { label: '执行中', color: 'processing' };
    case 'skipped':
      return { label: '已跳过', color: 'default' };
    default:
      return { label: status || '待检查', color: 'default' };
  }
}

/**
 * 格式化字节文件大小为易读单位（B / KB / MB / GB）。
 * @param bytes 原始字节数（为空或负数时按 0 处理）
 * @returns 格式化后的体积字符串；如 "12.4 MB"
 */
export function formatBytes(bytes?: number | null): string {
  if (bytes === undefined || bytes === null || Number.isNaN(bytes) || bytes <= 0) {
    return '0 B';
  }
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let val = bytes;
  let idx = 0;
  while (val >= 1024 && idx < units.length - 1) {
    val /= 1024;
    idx++;
  }
  return `${val >= 100 || idx === 0 ? val.toFixed(0) : val.toFixed(1)} ${units[idx]}`;
}

/**
 * 将秒数格式化为中文可读耗时（如 "14分25秒" 或 "1小时05分"）。
 * @param seconds 耗时秒数
 * @returns 中文耗时文本
 */
export function formatDuration(seconds?: number | null): string {
  if (!seconds || seconds <= 0) {
    return '0秒';
  }
  const total = Math.round(seconds);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) {
    return `${h}小时${m}分`;
  }
  if (m > 0) {
    return `${m}分${s}秒`;
  }
  return `${s}秒`;
}

/**
 * 格式化 ISO 时间字符串为本地简洁日期时间（YYYY-MM-DD HH:mm:ss）。
 * @param iso 时间字符串
 * @returns 本地时间字符串，为空或无效时返回 "-"
 */
export function formatDateTime(iso?: string | null): string {
  if (!iso) {
    return '-';
  }
  const d = new Date(iso);
  if (Number.isNaN(d.getTime()) || d.getFullYear() <= 1971) {
    return '-';
  }
  const pad = (n: number): string => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

/**
 * 安全解析 JSON 字符串数组（兼容已是 string[]、JSON 数组字符串、换行或逗号分隔文本）。
 * @param raw 原始输入值
 * @returns 去空后的字符串数组
 */
export function parseStringArray(raw: unknown): string[] {
  if (!raw) {
    return [];
  }
  if (Array.isArray(raw)) {
    return raw.map((item) => String(item).trim()).filter(Boolean);
  }
  if (typeof raw === 'string') {
    const trimmed = raw.trim();
    if (!trimmed) {
      return [];
    }
    if (trimmed.startsWith('[')) {
      try {
        const parsed = JSON.parse(trimmed);
        if (Array.isArray(parsed)) {
          return parsed.map((item) => String(item).trim()).filter(Boolean);
        }
      } catch {
        // 解析失败时降级按分隔符切分
      }
    }
    return trimmed
      .split(/[\n,]+/)
      .map((s) => s.trim())
      .filter(Boolean);
  }
  return [];
}

/**
 * 将多行或逗号分隔文本编码为紧凑 JSON 字符串数组。
 * @param text 用户在多行文本框中输入的路径或路由列表
 * @returns JSON 数组字符串，如 '["src/auth","src/common"]'
 */
export function toJsonArrayString(text: string): string {
  const arr = (text || '')
    .split(/[\n,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
  return JSON.stringify(arr);
}

/**
 * 复制指定文本到系统剪贴板（优先使用 Clipboard API，降级使用隐藏 textarea）。
 * @param text 待复制字符串
 * @returns 复制成功返回 Promise<boolean>
 */
export async function copyTextToClipboard(text: string): Promise<boolean> {
  if (!text) {
    return false;
  }
  try {
    if (navigator?.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // 降级使用传统选中复制方案
  }
  try {
    const textarea = document.createElement('textarea');
    textarea.value = text;
    textarea.style.position = 'fixed';
    textarea.style.opacity = '0';
    document.body.appendChild(textarea);
    textarea.select();
    const ok = document.execCommand('copy');
    document.body.removeChild(textarea);
    return ok;
  } catch {
    return false;
  }
}

/**
 * Antigravity CLI (`agy`) 预设可选模型列表（对齐 `agy models` 完整列表，空值表示跟随本机默认模型）。
 */
export const AGY_MODEL_OPTIONS: Array<{ value: string; label: string }> = [
  { value: '', label: '跟随本机 agy CLI 默认模型（当前默认：gemini-4-neon-high）' },
  { value: 'gemini-4-neon-high', label: 'Gemini 4 Neon (High) — gemini-4-neon-high' },
  { value: 'gemini-4-neon-xhigh', label: 'Gemini 4 Neon (XHigh) — gemini-4-neon-xhigh' },
  { value: 'gemini-4-neon-medium', label: 'Gemini 4 Neon (Medium) — gemini-4-neon-medium' },
  { value: 'gemini-4-neon-low', label: 'Gemini 4 Neon (Low) — gemini-4-neon-low' },
  { value: 'gemini-4-carbon-high', label: 'Gemini 4 Carbon (High) — gemini-4-carbon-high' },
  { value: 'gemini-4-carbon-medium', label: 'Gemini 4 Carbon (Medium) — gemini-4-carbon-medium' },
  { value: 'gemini-4-carbon-low', label: 'Gemini 4 Carbon (Low) — gemini-4-carbon-low' },
  { value: 'gemini-4-argon-high', label: 'Gemini 4 Argon (High) — gemini-4-argon-high' },
  { value: 'gemini-4-argon-medium', label: 'Gemini 4 Argon (Medium) — gemini-4-argon-medium' },
  { value: 'gemini-4-argon-low', label: 'Gemini 4 Argon (Low) — gemini-4-argon-low' },
  { value: 'gemini-4-hydrogen-xhigh', label: 'Gemini 4 Hydrogen (XHigh) — gemini-4-hydrogen-xhigh' },
  { value: 'gemini-4-hydrogen-high', label: 'Gemini 4 Hydrogen (High) — gemini-4-hydrogen-high' },
  { value: 'gemini-4-hydrogen-medium', label: 'Gemini 4 Hydrogen (Medium) — gemini-4-hydrogen-medium' },
  { value: 'gemini-4-hydrogen-low', label: 'Gemini 4 Hydrogen (Low) — gemini-4-hydrogen-low' },
  { value: 'claude-opus-5-5-high', label: 'Claude Opus 5.5 (High) — claude-opus-5-5-high' },
  { value: 'claude-opus-5-5-medium', label: 'Claude Opus 5.5 (Medium) — claude-opus-5-5-medium' },
  { value: 'claude-opus-5-5-low', label: 'Claude Opus 5.5 (Low) — claude-opus-5-5-low' },
  { value: 'claude-sonnet-5-5-high', label: 'Claude Sonnet 5.5 (High) — claude-sonnet-5-5-high' },
  { value: 'claude-sonnet-5-5-medium', label: 'Claude Sonnet 5.5 (Medium) — claude-sonnet-5-5-medium' },
  { value: 'claude-sonnet-5-5-low', label: 'Claude Sonnet 5.5 (Low) — claude-sonnet-5-5-low' },
  { value: 'gemini-pro-orchestrator-neon', label: 'Orchestrator (Neon) — gemini-pro-orchestrator-neon' },
  { value: 'gemini-pro-orchestrator', label: 'Orchestrator (Carbon) — gemini-pro-orchestrator' },
  { value: 'gemini-pro-orchestrator-argon', label: 'Orchestrator (Argon) — gemini-pro-orchestrator-argon' },
  { value: 'gemini-3.8-flash-high', label: 'Gemini 3.8 Flash (High) — gemini-3.8-flash-high' },
  { value: 'gemini-3.8-flash-medium', label: 'Gemini 3.8 Flash (Medium) — gemini-3.8-flash-medium' },
  { value: 'gemini-3.8-flash-low', label: 'Gemini 3.8 Flash (Low) — gemini-3.8-flash-low' },
  { value: 'gpt-oss-120b-medium', label: 'GPT-OSS 120B (Medium) — gpt-oss-120b-medium' }
];

/**
 * Antigravity CLI (`agy`) 推理强度（`--effort`）可选项。
 */
export const AGY_EFFORT_OPTIONS: Array<{ value: string; label: string }> = [
  { value: '', label: '默认（跟随所选模型档位）' },
  { value: 'low', label: 'low（快速）' },
  { value: 'medium', label: 'medium（标准）' },
  { value: 'high', label: 'high（深度推理）' },
  { value: 'xhigh', label: 'xhigh（超深度推理）' },
  { value: 'max', label: 'max（最高强度）' }
];

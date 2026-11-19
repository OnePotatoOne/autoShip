/**
 * autoShip 管理后台全局 API 契约与业务领域数据模型定义（UTF-8）。
 * 严格对齐后端 `/api/v1/admin/*` 统一响应信封与分页规范。
 */

/**
 * 后端统一 JSON 响应信封接口。
 */
export interface ApiEnvelope<T> {
  requestId?: string;
  code: number;
  message: string;
  data: T;
}

/**
 * 后端标准分页返回结构。
 */
export interface PageResult<T> {
  total: number;
  page: number;
  pageSize: number;
  list: T[];
}

/**
 * 管理后台登录管理员用户信息。
 */
export interface AdminUser {
  id: number;
  username: string;
  otpEnabled: boolean;
  lastLoginAt?: string;
  lastLoginIp?: string;
}

/**
 * 认证状态接口 `/api/v1/admin/auth/status` 响应载荷。
 */
export interface AuthStatusData {
  dbReady?: boolean;
  initialized: boolean;
  authenticated: boolean;
  caFingerprint?: string;
  dbConfig?: {
    host: string;
    port: number;
    user: string;
    dbName: string;
  };
  user?: AdminUser;
  csrfToken?: string;
}

/**
 * 首次初始化接口 `/api/v1/admin/auth/setup` 响应载荷。
 */
export interface AuthSetupResult {
  user: AdminUser;
  otpSecret: string;
  otpUri: string;
  recoveryCodes: string[];
  csrfToken: string;
}

/**
 * 管理员登录接口 `/api/v1/admin/auth/login` 响应载荷。
 */
export interface AuthLoginResult {
  user: AdminUser;
  otpSetupRequired: boolean;
  csrfToken: string;
}

/**
 * OTP 预绑定接口 `/api/v1/admin/auth/otp/setup` 响应载荷。
 */
export interface OtpSetupResult {
  secret: string;
  uri: string;
  recoveryCodes: string[];
}

/**
 * 总览看板按产品聚合统计项。
 */
export interface ProductOverviewCount {
  productId: number;
  productName: string;
  total: number;
  inProgress: number;
  deployed: number;
}

/**
 * 总览看板近期开发任务用量统计项。
 */
export interface WorkUsageItem {
  workItemId: number;
  title: string;
  promptTokens: number;
  completionTokens: number;
  totalTokens: number;
  durationSeconds: number;
  status: string;
}

/**
 * 总览看板 `/api/v1/admin/overview` 完整响应结构。
 */
export interface OverviewData {
  counts: {
    pendingRequirements: number;
    inProgressWork: number;
    pendingAcceptance: number;
    pendingDeploy: number;
    totalProducts: number;
    byProduct: ProductOverviewCount[];
  };
  llmStatus: {
    mode: string;
    healthy: boolean;
    relayAgentOnline: boolean;
    lastCheckedAt?: string;
  };
  agents: AgentNode[];
  recentReleases: ReleaseItem[];
  recentDeployments: DeploymentItem[];
  recentWorkUsage: WorkUsageItem[];
}

/**
 * 产品主档案配置模型。
 */
export interface Product {
  id: number;
  code: string;
  name: string;
  description: string;
  repoUrl: string;
  defaultBranch: string;
  installCmd: string;
  buildCmd: string;
  testCmd: string;
  lintCmd: string;
  commandTimeoutSec: number;
  previewStartCmd: string;
  previewPortCount: number;
  previewDbPrefix: string;
  previewSeedCmd: string;
  previewSupportsTls: boolean;
  healthCheckPath: string;
  changelogFile: string;
  deployHookPath: string;
  releaseSignPubB64: string;
  devRulesPrompt: string;
  intakeEnabled: boolean;
  autoDevEnabled: boolean;
  autoDeployEnabled: boolean;
  planConfirmDefault: boolean;
  createdAt?: string;
  updatedAt?: string;
}

/**
 * 产品业务模块定义。
 */
export interface ProductModule {
  id?: number;
  productId?: number;
  code: string;
  name: string;
  pathsJson: string;
  testCmd: string;
  pageUrlsJson: string;
  description: string;
}

/**
 * 产品代码改动范围与公共区规则。
 */
export interface ProductScopeRule {
  id?: number;
  productId?: number;
  zoneType: string;
  pathPattern: string;
  note: string;
}

/**
 * 产品开发 Agent 沙箱权限档案。
 */
export interface ProductPermissionProfile {
  id?: number;
  productId?: number;
  mode: string;
  allowedWritePathsJson?: string;
  extraDeniedReadJson?: string;
  allowedWritePaths?: string[];
  extraDeniedReadPaths?: string[];
  lastBlockedPath?: string;
  updatedAt?: string;
}

/**
 * 产品领域知识条目。
 */
export interface ProductKnowledge {
  id?: number;
  productId?: number;
  category: string;
  title: string;
  content: string;
  source: string;
  updatedAt?: string;
}

/**
 * 单个产品的完整聚合配置档案。
 */
export interface ProductFullBundle {
  product: Product;
  modules: ProductModule[];
  scopeRules: ProductScopeRule[];
  permissionProfile?: ProductPermissionProfile | null;
  knowledge: ProductKnowledge[];
}

/**
 * 需求对象模型（含进行中时关联的开发任务、版本、部署三对象状态兼容字段）。
 */
export interface RequirementItem {
  id: number;
  productId: number;
  productName?: string;
  moduleCode: string;
  issueType: string;
  title: string;
  priority: string;
  effortSize?: string;
  readinessScore?: number;
  status: string;
  description?: string;
  acceptanceCriteria?: string;
  expectedBehavior?: string;
  actualBehavior?: string;
  reproSteps?: string;
  outOfScope?: string;
  summaryCardJson?: string;
  envInfoJson?: string;
  tagsCsv?: string;
  archived?: boolean;
  closeReason?: string;
  closeNote?: string;
  activeWorkItemId?: number;
  workStatus?: string;
  workItemStatus?: string;
  releasedVersion?: string;
  releaseVersion?: string;
  releaseStatus?: string;
  deployStatus?: string;
  deploymentStatus?: string;
  currentStates?: {
    workItemId?: number;
    workItemStatus?: string;
    releaseId?: number;
    releaseVersion?: string;
    releaseStatus?: string;
    deploymentId?: number;
    deploymentStatus?: string;
  };
  createdAt?: string;
  updatedAt?: string;
  [key: string]: any;
}

/**
 * 手动录入需求提交参数。
 */
export interface CreateRequirementPayload {
  productId: number;
  moduleCode: string;
  issueType: string;
  title: string;
  priority: string;
  status?: string;
  description: string;
  acceptanceCriteria: string;
  expectedBehavior: string;
  reproSteps: string;
}

/**
 * 需求详情接口 `/api/v1/admin/requirements/:id` 返回结构。
 */
export interface RequirementDetailBundle {
  requirement: RequirementItem;
  conversation?: Record<string, any> | null;
  messages: Array<Record<string, any>>;
  prototypes: Array<Record<string, any>>;
  similarLinks: Array<Record<string, any>>;
  reporters: Array<Record<string, any>>;
  events: Array<Record<string, any>>;
  workItem?: WorkItem | null;
  release?: ReleaseItem | null;
  deployment?: DeploymentItem | null;
}

/**
 * 开发任务模型。
 */
export interface WorkItem {
  id: number;
  productId: number;
  productName?: string;
  requirementId?: number;
  title: string;
  branchName?: string;
  status: string;
  stage?: string;
  moduleCode?: string;
  planConfirmRequired?: boolean;
  planSummary?: string;
  planModulesJson?: string;
  planPublicFilesJson?: string;
  planSchemaChanges?: string;
  hasSchemaChange?: boolean;
  migrationRisk?: string;
  pendingQuestionId?: string;
  pendingQuestion?: string;
  systemImpactConclusion?: string;
  affectedModulesJson?: string;
  reviewConclusion?: string;
  changeSummary?: string;
  keyDiff?: string;
  suggestedVersion?: string;
  headCommit?: string;
  tokenBudget?: number;
  promptTokens?: number;
  completionTokens?: number;
  totalTokens?: number;
  maxTurns?: number;
  usedTurns?: number;
  maxDurationMinutes?: number;
  durationSeconds?: number;
  acceptanceComment?: string;
  createdAt?: string;
  updatedAt?: string;
  [key: string]: any;
}

/**
 * 开发任务代码改动范围记录（本模块区 / 其他模块区 / 公共区）。
 */
export interface WorkScopeChange {
  id?: number;
  workItemId?: number;
  zone?: string;
  zoneType?: string;
  moduleCode?: string;
  filePath: string;
  addedLines?: number;
  deletedLines?: number;
  linesChanged?: number;
  reason?: string;
  isTestFile?: boolean;
  isTestModified?: boolean;
  impactAuditNote?: string;
}

/**
 * 开发任务门禁检查结果项。
 */
export interface WorkGate {
  id?: number;
  gateCode?: string;
  name?: string;
  gateName?: string;
  status: string;
  exitCode?: number;
  summary?: string;
  outputExcerpt?: string;
  evidence?: string;
  evidenceJson?: string;
  checkedAt?: string;
  updatedAt?: string;
}

/**
 * 开发任务预览会话信息（Mac Tailnet 直连地址与随机测试账号）。
 */
export interface PreviewSession {
  id?: number;
  workItemId?: number;
  previewUrl?: string;
  url?: string;
  tailnetUrl?: string;
  port?: number;
  status?: string;
  testUsername?: string;
  testPassword?: string;
  testAccountJson?: string;
  expiresAt?: string;
  startedAt?: string;
}

/**
 * 开发任务详情接口 `/api/v1/admin/work-items/:id` 完整返回结构。
 */
export interface WorkItemDetailBundle {
  workItem: WorkItem;
  requirements: RequirementItem[];
  gates: WorkGate[];
  scopeChanges: WorkScopeChange[];
  activities: Array<Record<string, any>>;
  transcripts: Array<Record<string, any>>;
  previewSession?: PreviewSession | null;
}

/**
 * 版本发布记录模型。
 */
export interface ReleaseItem {
  id: number;
  productId: number;
  productName?: string;
  workItemId?: number;
  version: string;
  tagName?: string;
  commitSha?: string;
  status: string;
  migrationRisk?: string;
  changelog?: string;
  ciRunUrl?: string;
  ciStatus?: string;
  minAgentVersion?: string;
  manifestSigB64?: string;
  failureReason?: string;
  publishedAt?: string;
  createdAt?: string;
  [key: string]: any;
}

/**
 * 版本制品清单项。
 */
export interface ReleaseArtifact {
  id?: number;
  releaseId?: number;
  fileName: string;
  sizeBytes?: number;
  fileSize?: number;
  sha256: string;
  signatureB64?: string;
  verified?: boolean;
  createdAt?: string;
}

/**
 * 版本详情 `/api/v1/admin/releases/:id` 返回结构。
 */
export interface ReleaseDetailBundle {
  release: ReleaseItem;
  artifacts: ReleaseArtifact[];
  requirements: RequirementItem[];
  deployments: DeploymentItem[];
  deployTargets: DeployTarget[];
}

/**
 * 生产部署目标节点模型。
 */
export interface DeployTarget {
  id: number;
  productId: number;
  productName?: string;
  name: string;
  env?: string;
  environment?: string;
  agentId?: number;
  agentName?: string;
  agentOnline?: boolean;
  agentVersion?: string;
  currentVersion?: string;
  serviceStatus?: string;
  serverIp?: string;
  healthCheckUrl?: string;
  backupDir?: string;
  backupQuotaMb?: number;
  backupQuotaBytes?: number;
  backupUsedMb?: number;
  backupUsedBytes?: number;
  backupRetainCount?: number;
  diskFreeMb?: number;
  diskFreeBytes?: number;
  storageExceeded?: boolean;
  lastHeartbeatAt?: string;
  createdAt?: string;
  [key: string]: any;
}

/**
 * 部署记录模型。
 */
export interface DeploymentItem {
  id: number;
  productId: number;
  productName?: string;
  releaseId: number;
  targetId: number;
  targetName?: string;
  version?: string;
  targetVersion?: string;
  fromVersion?: string;
  previousVersion?: string;
  backupDb?: boolean;
  status: string;
  failedStep?: string;
  exitCode?: number;
  errorExcerpt?: string;
  healthCheckResp?: string;
  triggeredBy?: string;
  startedAt?: string;
  finishedAt?: string;
  createdAt?: string;
  [key: string]: any;
}

/**
 * 部署分步执行日志项。
 */
export interface DeploymentStep {
  id?: number;
  deploymentId?: number;
  stepNo?: number;
  stepCode?: string;
  stepName?: string;
  name?: string;
  status: string;
  exitCode?: number;
  message?: string;
  logOutput?: string;
  output?: string;
  startedAt?: string;
  finishedAt?: string;
  createdAt?: string;
  updatedAt?: string;
  [key: string]: any;
}

/**
 * 数据库备份记录模型。
 */
export interface DbBackupItem {
  id: number;
  productId: number;
  productName?: string;
  targetId: number;
  targetName?: string;
  deploymentId?: number;
  backupDir: string;
  filesJson?: string;
  sizeBytes?: number;
  sizeMb?: number;
  checksumSha256?: string;
  status?: string;
  restoreGuide?: string;
  storageExceeded?: boolean;
  createdAt?: string;
  [key: string]: any;
}

/**
 * 部署详情接口 `/api/v1/admin/deployments/:id` 返回结构。
 */
export interface DeploymentDetailBundle {
  deployment: DeploymentItem;
  steps: DeploymentStep[];
  backups: DbBackupItem[];
}

/**
 * 外部业务系统接入方模型。
 */
export interface IntegrationItem {
  id: number;
  name: string;
  appId: string;
  plainAppSecret?: string;
  allowedIpsCsv: string;
  rateLimitRpm: number;
  enabled: boolean;
  productIds?: number[];
  createdAt?: string;
  updatedAt?: string;
}

/**
 * Agent 节点与注册令牌模型。
 */
export interface AgentNode {
  id: number;
  name: string;
  agentType?: string;
  type?: string;
  role?: string;
  productId?: number;
  targetId?: number;
  status?: string;
  online?: boolean;
  agentVersion?: string;
  version?: string;
  productVersion?: string;
  publicKeyB64?: string;
  diskFreeMb?: number;
  backupUsedMb?: number;
  lastHeartbeatAt?: string;
  revokedAt?: string;
  createdAt?: string;
  [key: string]: any;
}

/**
 * 生成一次性 Agent 注册令牌（15 分钟有效）返回结构。
 */
export interface AgentEnrollTokenResult {
  agent?: AgentNode;
  registrationToken?: string;
  token?: string;
  enrollToken?: string;
  agentId?: number;
  name?: string;
  agentType?: string;
  expiresAt: string;
  caFingerprint?: string;
}

/**
 * 系统全局设置视图模型（对齐 `/api/v1/admin/settings`）。
 */
export interface SystemSettingsView {
  llm: {
    mode: string;
    directBaseURL: string;
    hasDirectApiKey: boolean;
    intakeModel: string;
    triageModel: string;
    maxSessionTokens: number;
    maxRequestTokens: number;
    maskPIIBeforeSend: boolean;
  };
  notify: {
    feishuWebhookURL: string;
    enabled: boolean;
  };
  workflow?: {
    autoStartOnNewRequirement: boolean;
    autoStartOnConfirmStatus: boolean;
    defaultPlanConfirm: boolean;
    defaultAgyModel?: string;
    defaultAgyEffort?: string;
    defaultTokenBudget: number;
    defaultMaxTurns: number;
    defaultMaxMinutes: number;
  };
  pki: {
    caFingerprint: string;
    publicListenAddr: string;
    publicAllowIPs?: string[];
    tailnetListenAddr: string;
    serverSignPubB64: string;
  };
  artifactKeepCount: number;
}

/**
 * 更新系统全局设置提交载荷。
 */
export interface UpdateSystemSettingsPayload {
  llm: {
    mode: string;
    directBaseURL: string;
    directApiKey: string;
    intakeModel: string;
    triageModel: string;
    maxSessionTokens: number;
    maxRequestTokens: number;
    maskPIIBeforeSend: boolean;
  };
  notify: {
    feishuWebhookURL: string;
    enabled: boolean;
  };
  workflow?: {
    autoStartOnNewRequirement: boolean;
    autoStartOnConfirmStatus: boolean;
    defaultPlanConfirm: boolean;
    defaultAgyModel?: string;
    defaultAgyEffort?: string;
    defaultTokenBudget: number;
    defaultMaxTurns: number;
    defaultMaxMinutes: number;
  };
  publicListenAddr?: string;
  publicAllowIPs?: string[];
  artifactKeepCount: number;
}

/**
 * 全量操作审计日志模型。
 */
export interface AuditLogItem {
  id: number;
  productId: number;
  actorType: string;
  actorId: string;
  module: string;
  action: string;
  targetType: string;
  targetId: string;
  method: string;
  path: string;
  clientIp: string;
  statusCode: number;
  errorMsg: string;
  detailJson: string;
  createdAt: string;
}

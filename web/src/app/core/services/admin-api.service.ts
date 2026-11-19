import { Injectable, inject } from '@angular/core';
import { HttpClient, HttpErrorResponse, HttpParams } from '@angular/common/http';
import { Observable, catchError, map, throwError } from 'rxjs';
import {
  AgentEnrollTokenResult,
  AgentNode,
  ApiEnvelope,
  AuditLogItem,
  CreateRequirementPayload,
  DbBackupItem,
  DeployTarget,
  DeploymentDetailBundle,
  DeploymentItem,
  IntegrationItem,
  OverviewData,
  PageResult,
  Product,
  ProductFullBundle,
  ProductKnowledge,
  ProductModule,
  ProductPermissionProfile,
  ProductScopeRule,
  ReleaseDetailBundle,
  ReleaseItem,
  RequirementDetailBundle,
  RequirementItem,
  SystemSettingsView,
  UpdateSystemSettingsPayload,
  WorkItem,
  WorkItemDetailBundle
} from '../models/api.models';

/**
 * autoShip 管理后台业务 API 统一访问服务。
 * 100% 对齐 `/api/v1/admin/*` 各模块路径、查询参数、请求体与响应信封解包逻辑。
 * 在各业务组件中通过 `inject(AdminApiService)` 调用。
 */
@Injectable({ providedIn: 'root' })
export class AdminApiService {
  private readonly http = inject(HttpClient);

  /**
   * 统一解包后端 `{ code, message, data }` 信封，并在业务异常或 HTTP 错误时提取可读中文错误提示。
   * @param source 原始 HttpClient 返回的信封 Observable
   * @returns 解包后的业务数据 `T` Observable
   */
  private unwrap<T>(source: Observable<ApiEnvelope<T>>): Observable<T> {
    return source.pipe(
      map((env) => {
        if (!env || typeof env.code !== 'number') {
          return (env as unknown as T) ?? ({} as T);
        }
        if (env.code !== 0) {
          throw new Error(env.message || `请求失败 (错误码 ${env.code})`);
        }
        return env.data;
      }),
      catchError((err: unknown) => {
        if (err instanceof HttpErrorResponse) {
          const serverMsg = err.error?.message;
          if (typeof serverMsg === 'string' && serverMsg.trim()) {
            return throwError(() => new Error(serverMsg));
          }
          return throwError(() => new Error(`网络或服务响应异常 (HTTP ${err.status || '连接失败'})`));
        }
        if (err instanceof Error) {
          return throwError(() => err);
        }
        return throwError(() => new Error('请求处理失败'));
      })
    );
  }

  /**
   * 构造过滤掉空值与 undefined 的 HttpParams 查询参数对象。
   * @param query 键值对查询条件对象
   * @returns 组装后的 `HttpParams` 实例
   */
  private buildParams(query: Record<string, string | number | boolean | undefined | null>): HttpParams {
    let params = new HttpParams();
    for (const [k, v] of Object.entries(query)) {
      if (v !== undefined && v !== null && String(v).trim() !== '') {
        params = params.set(k, String(v).trim());
      }
    }
    return params;
  }

  /**
   * 将后端列表或分页对象归一化为统一的 `PageResult<T>` 结构。
   * @param raw 后端返回的原始分页或数组载荷
   * @returns 标准化的 `PageResult<T>`
   */
  private normalizePage<T>(raw: PageResult<T> | T[] | null | undefined): PageResult<T> {
    if (Array.isArray(raw)) {
      return { total: raw.length, page: 1, pageSize: raw.length || 20, list: raw };
    }
    return {
      total: Number(raw?.total ?? 0),
      page: Number(raw?.page ?? 1),
      pageSize: Number(raw?.pageSize ?? 20),
      list: Array.isArray(raw?.list) ? raw!.list : []
    };
  }

  // ==================== 1. 总览看板 ====================

  /**
   * 获取平台全局运行总览数据（计数、模型通道健康度、Agent 在线状态、最近发布部署与任务用量）。
   * @returns `OverviewData` 数据流；供 `/dashboard` 与顶栏调用
   */
  getOverview(): Observable<OverviewData> {
    return this.unwrap(this.http.get<ApiEnvelope<OverviewData>>('/api/v1/admin/overview'));
  }

  // ==================== 2. 需求池管理 ====================

  /**
   * 分页筛选查询需求列表。
   * @param filter 包含产品 ID、模块编码、类型、状态、关键词及分页页码的查询条件
   * @returns 需求分页列表 `PageResult<RequirementItem>`
   */
  listRequirements(filter: {
    productId?: number | string;
    moduleCode?: string;
    issueType?: string;
    status?: string;
    keyword?: string;
    page?: number;
    pageSize?: number;
  }): Observable<PageResult<RequirementItem>> {
    return this.unwrap(
      this.http.get<ApiEnvelope<PageResult<RequirementItem>>>('/api/v1/admin/requirements', {
        params: this.buildParams(filter)
      })
    ).pipe(map((res) => this.normalizePage(res)));
  }

  /**
   * 在后台手动录入新需求。
   * @param payload 手动录入需求结构化表单载荷
   * @returns 创建成功的 `RequirementItem`
   */
  createRequirement(payload: CreateRequirementPayload): Observable<RequirementItem> {
    return this.unwrap(
      this.http.post<ApiEnvelope<RequirementItem>>('/api/v1/admin/requirements', payload)
    );
  }

  /**
   * 获取单条需求的完整详情聚合包（含澄清对话、结构化需求卡、沙箱原型、相似需求关联与时间线）。
   * @param id 需求主键 ID
   * @returns `RequirementDetailBundle` 详情对象
   */
  getRequirementDetail(id: number): Observable<RequirementDetailBundle> {
    return this.unwrap(
      this.http.get<ApiEnvelope<RequirementDetailBundle>>(`/api/v1/admin/requirements/${id}`)
    );
  }

  /**
   * 对需求执行人工分拣定级。
   * @param id 需求主键 ID
   * @param payload 分拣类型、归属模块、优先级、工作量估计、可开工度评分与目标状态
   * @returns 更新后的需求记录
   */
  triageRequirement(
    id: number,
    payload: {
      issueType: string;
      moduleCode: string;
      priority: string;
      effortSize: string;
      readinessScore: number;
      status: string;
    }
  ): Observable<RequirementItem> {
    return this.unwrap(
      this.http.post<ApiEnvelope<RequirementItem>>(`/api/v1/admin/requirements/${id}/triage`, payload)
    );
  }

  /**
   * 手动修改单条需求的业务状态，并支持在确认进入研发状态时联动启动开发任务。
   * @param id 需求主键 ID
   * @param payload 目标状态编码、是否立即启动开发任务、方案确认门开关、关闭原因与备注
   * @returns 返回包含更新后 `requirement` 与可选 `workItemId` 的结果流；供需求池状态弹窗与确认立项按钮调用
   */
  updateRequirementStatus(
    id: number,
    payload: {
      status: string;
      triggerStartWork?: boolean;
      planConfirmRequired?: boolean;
      agyModel?: string;
      agyEffort?: string;
      closeReason?: string;
      note?: string;
    }
  ): Observable<{ requirement: RequirementItem; workItemId?: number }> {
    return this.unwrap(
      this.http.post<ApiEnvelope<{ requirement: RequirementItem; workItemId?: number }>>(
        `/api/v1/admin/requirements/${id}/status`,
        payload
      )
    );
  }

  /**
   * 建立当前需求与另一相似/重复需求的关联关系。
   * @param id 当前需求主键 ID
   * @param payload 目标需求 ID 与关联类型（如 similar / duplicate / related）
   * @returns 关联结果响应
   */
  linkRequirement(
    id: number,
    payload: { targetRequirementId: number; linkType: string }
  ): Observable<Record<string, any>> {
    return this.unwrap(
      this.http.post<ApiEnvelope<Record<string, any>>>(`/api/v1/admin/requirements/${id}/link`, payload)
    );
  }

  /**
   * 关闭指定需求并归档原因。
   * @param id 需求主键 ID
   * @param payload 关闭原因（rejected/duplicate/deferred/answered）与备注说明
   * @returns 更新后的需求对象
   */
  closeRequirement(
    id: number,
    payload: { reason: 'rejected' | 'duplicate' | 'deferred' | 'answered'; note: string }
  ): Observable<RequirementItem> {
    return this.unwrap(
      this.http.post<ApiEnvelope<RequirementItem>>(`/api/v1/admin/requirements/${id}/close`, payload)
    );
  }

  /**
   * 对需求点击“开工”，创建并下发自动开发任务到本 Mac 开发 Agent。
   * @param id 需求主键 ID
   * @param payload 方案确认门开关及 Token / 轮数 / 时长预算上限
   * @returns 创建的开发任务或响应载荷
   */
  startWorkForRequirement(
    id: number,
    payload: {
      planConfirmRequired: boolean;
      tokenBudget: number;
      maxTurns: number;
      maxDurationMinutes: number;
      agyModel?: string;
      agyEffort?: string;
    }
  ): Observable<Record<string, any>> {
    return this.unwrap(
      this.http.post<ApiEnvelope<Record<string, any>>>(
        `/api/v1/admin/requirements/${id}/start-work`,
        payload
      )
    );
  }

  // ==================== 3. 开发任务管理 ====================

  /**
   * 分页查询开发任务列表。
   * @param filter 产品 ID、任务状态及分页参数
   * @returns 开发任务分页列表 `PageResult<WorkItem>`
   */
  listWorkItems(filter: {
    productId?: number | string;
    status?: string;
    page?: number;
    pageSize?: number;
  }): Observable<PageResult<WorkItem>> {
    return this.unwrap(
      this.http.get<ApiEnvelope<PageResult<WorkItem>>>('/api/v1/admin/work-items', {
        params: this.buildParams(filter)
      })
    ).pipe(map((res) => this.normalizePage(res)));
  }

  /**
   * 获取单个开发任务的完整详情与验收包（含改动范围三区、门禁结果、活动流、会话引用与预览实例）。
   * @param id 开发任务主键 ID
   * @returns `WorkItemDetailBundle` 详情对象
   */
  getWorkItemDetail(id: number): Observable<WorkItemDetailBundle> {
    return this.unwrap(
      this.http.get<ApiEnvelope<WorkItemDetailBundle>>(`/api/v1/admin/work-items/${id}`)
    );
  }

  /**
   * 审批开发任务的技术方案（同意后进入编码会话，退回则重拟方案）。
   * @param id 开发任务主键 ID
   * @param payload 是否同意 `approved` 与审批意见 `comment`
   * @returns 更新后的任务状态对象
   */
  confirmWorkPlan(id: number, payload: { approved: boolean; comment: string }): Observable<WorkItem> {
    return this.unwrap(
      this.http.post<ApiEnvelope<WorkItem>>(`/api/v1/admin/work-items/${id}/confirm-plan`, payload)
    );
  }

  /**
   * 回答开发 Agent 在 `ask_owner` 阶段提出的待决策问题。
   * @param id 开发任务主键 ID
   * @param payload 问题编号 `questionId` 与人工答复内容 `answer`
   * @returns 答复提交结果
   */
  answerWorkQuestion(
    id: number,
    payload: { questionId: string; answer: string }
  ): Observable<Record<string, any>> {
    return this.unwrap(
      this.http.post<ApiEnvelope<Record<string, any>>>(`/api/v1/admin/work-items/${id}/answer`, payload)
    );
  }

  /**
   * 人工验收通过开发任务并触发自动发布流水线。
   * @param id 开发任务主键 ID
   * @param payload 确认版本号、破坏性迁移特批开关及验收评语
   * @returns 发布触发结果
   */
  acceptWorkItem(
    id: number,
    payload: { version: string; approveDestructiveMigration: boolean; comment: string }
  ): Observable<Record<string, any>> {
    return this.unwrap(
      this.http.post<ApiEnvelope<Record<string, any>>>(`/api/v1/admin/work-items/${id}/accept`, payload)
    );
  }

  /**
   * 打回待验收的开发任务，附修改意见回到编码会话继续迭代。
   * @param id 开发任务主键 ID
   * @param payload 打回修改意见
   * @returns 更新后的任务对象
   */
  rejectWorkItem(id: number, payload: { comment: string }): Observable<WorkItem> {
    return this.unwrap(
      this.http.post<ApiEnvelope<WorkItem>>(`/api/v1/admin/work-items/${id}/reject`, payload)
    );
  }

  /**
   * 放弃指定开发任务（关联需求自动退回待排期状态）。
   * @param id 开发任务主键 ID
   * @param payload 放弃原因备注
   * @returns 更新后的任务对象
   */
  abandonWorkItem(id: number, payload: { comment: string }): Observable<WorkItem> {
    return this.unwrap(
      this.http.post<ApiEnvelope<WorkItem>>(`/api/v1/admin/work-items/${id}/abandon`, payload)
    );
  }

  // ==================== 4. 版本发布管理 ====================

  /**
   * 分页查询版本发布列表。
   * @param filter 产品 ID、版本状态及分页参数
   * @returns 版本分页数据 `PageResult<ReleaseItem>`
   */
  listReleases(filter: {
    productId?: number | string;
    status?: string;
    page?: number;
    pageSize?: number;
  }): Observable<PageResult<ReleaseItem>> {
    return this.unwrap(
      this.http.get<ApiEnvelope<PageResult<ReleaseItem>>>('/api/v1/admin/releases', {
        params: this.buildParams(filter)
      })
    ).pipe(map((res) => this.normalizePage(res)));
  }

  /**
   * 获取单个版本的完整详情（含制品包清单、关联需求、历史部署及可选部署目标列表）。
   * @param id 版本主键 ID
   * @returns `ReleaseDetailBundle` 详情对象
   */
  getReleaseDetail(id: number): Observable<ReleaseDetailBundle> {
    return this.unwrap(
      this.http.get<ApiEnvelope<ReleaseDetailBundle>>(`/api/v1/admin/releases/${id}`)
    );
  }

  /**
   * 针对 `publish_failed` 发布失败的版本新建关联修复开发任务（修好后以新版本号重发，不移动已有 tag）。
   * @param id 发布失败的版本主键 ID
   * @param payload 修复备注说明
   * @returns 新建的关联修复开发任务信息
   */
  createFixWorkForRelease(id: number, payload: { note: string }): Observable<Record<string, any>> {
    return this.unwrap(
      this.http.post<ApiEnvelope<Record<string, any>>>(
        `/api/v1/admin/releases/${id}/create-fix-work`,
        payload
      )
    );
  }

  /**
   * 人工确认部署版本，向选定的产品服务器更新 Agent 下发升级指令。
   * @param id 版本主键 ID
   * @param payload 目标服务器 ID 列表与是否先备份数据库标志
   * @returns 创建的部署记录结果
   */
  confirmReleaseDeploy(
    id: number,
    payload: { targetIds: number[]; backupDb: boolean }
  ): Observable<Record<string, any>> {
    return this.unwrap(
      this.http.post<ApiEnvelope<Record<string, any>>>(
        `/api/v1/admin/releases/${id}/confirm-deploy`,
        payload
      )
    );
  }

  // ==================== 5. 部署中心与数据库备份 ====================

  /**
   * 查询部署目标节点列表。
   * @param productId 可选产品 ID 筛选
   * @returns 部署目标数组 `DeployTarget[]`
   */
  listDeployTargets(productId?: number | string): Observable<DeployTarget[]> {
    return this.unwrap(
      this.http.get<ApiEnvelope<DeployTarget[] | PageResult<DeployTarget>>>(
        '/api/v1/admin/deploy-targets',
        { params: this.buildParams({ productId }) }
      )
    ).pipe(map((res) => (Array.isArray(res) ? res : res?.list ?? [])));
  }

  /**
   * 新建生产部署目标节点。
   * @param payload 部署目标配置字段
   * @returns 新建后的 `DeployTarget`
   */
  createDeployTarget(payload: Partial<DeployTarget>): Observable<DeployTarget> {
    return this.unwrap(
      this.http.post<ApiEnvelope<DeployTarget>>('/api/v1/admin/deploy-targets', payload)
    );
  }

  /**
   * 更新已有生产部署目标节点配置。
   * @param id 部署目标主键 ID
   * @param payload 更新后的目标配置字段
   * @returns 更新后的 `DeployTarget`
   */
  updateDeployTarget(id: number, payload: Partial<DeployTarget>): Observable<DeployTarget> {
    return this.unwrap(
      this.http.put<ApiEnvelope<DeployTarget>>(`/api/v1/admin/deploy-targets/${id}`, payload)
    );
  }

  /**
   * 删除指定部署目标节点。
   * @param id 部署目标主键 ID
   * @returns 删除结果响应
   */
  deleteDeployTarget(id: number): Observable<Record<string, any>> {
    return this.unwrap(
      this.http.delete<ApiEnvelope<Record<string, any>>>(`/api/v1/admin/deploy-targets/${id}`)
    );
  }

  /**
   * 向指定部署目标的更新 Agent 下发手动数据库备份指令。
   * @param id 部署目标主键 ID
   * @returns 备份指令下发结果
   */
  triggerTargetBackup(id: number): Observable<Record<string, any>> {
    return this.unwrap(
      this.http.post<ApiEnvelope<Record<string, any>>>(`/api/v1/admin/deploy-targets/${id}/backup`, {})
    );
  }

  /**
   * 分页查询部署执行记录列表。
   * @param filter 产品 ID、目标节点 ID、状态及分页条件
   * @returns 部署记录分页结构 `PageResult<DeploymentItem>`
   */
  listDeployments(filter: {
    productId?: number | string;
    targetId?: number | string;
    status?: string;
    page?: number;
    pageSize?: number;
  }): Observable<PageResult<DeploymentItem>> {
    return this.unwrap(
      this.http.get<ApiEnvelope<PageResult<DeploymentItem>>>('/api/v1/admin/deployments', {
        params: this.buildParams(filter)
      })
    ).pipe(map((res) => this.normalizePage(res)));
  }

  /**
   * 查询单次部署的完整分步日志与关联备份记录。
   * @param id 部署记录主键 ID
   * @returns `DeploymentDetailBundle` 详情对象
   */
  getDeploymentDetail(id: number): Observable<DeploymentDetailBundle> {
    return this.unwrap(
      this.http.get<ApiEnvelope<DeploymentDetailBundle>>(`/api/v1/admin/deployments/${id}`)
    );
  }

  /**
   * 对指定部署记录触发手动回滚（按部署 ID 幂等回退至该次升级前的程序备份）。
   * @param id 部署记录主键 ID
   * @param payload 手动回滚原因说明
   * @returns 回滚指令下发响应
   */
  rollbackDeployment(id: number, payload: { reason: string }): Observable<Record<string, any>> {
    return this.unwrap(
      this.http.post<ApiEnvelope<Record<string, any>>>(
        `/api/v1/admin/deployments/${id}/rollback`,
        payload
      )
    );
  }

  /**
   * 查询数据库备份记录列表及存储配额告警信息。
   * @param filter 可选按产品 ID、部署目标 ID 过滤
   * @returns 归一化后的备份列表与可选汇总信息
   */
  listBackups(filter: {
    productId?: number | string;
    targetId?: number | string;
  }): Observable<{ list: DbBackupItem[]; storageExceeded: boolean; raw: any }> {
    return this.unwrap(
      this.http.get<ApiEnvelope<any>>('/api/v1/admin/backups', {
        params: this.buildParams(filter)
      })
    ).pipe(
      map((res) => {
        if (Array.isArray(res)) {
          const exceeded = res.some((b) => Boolean(b.storageExceeded));
          return { list: res as DbBackupItem[], storageExceeded: exceeded, raw: res };
        }
        const list: DbBackupItem[] = Array.isArray(res?.list)
          ? res.list
          : Array.isArray(res?.backups)
            ? res.backups
            : [];
        const exceeded =
          Boolean(res?.storageExceeded) ||
          Boolean(res?.quotaExceeded) ||
          list.some((b) => Boolean(b.storageExceeded));
        return { list, storageExceeded: exceeded, raw: res };
      })
    );
  }

  // ==================== 6. 设置中心：产品档案、接入方、Agent、全局设置与审计 ====================

  /**
   * 获取全部纳管产品主档案列表。
   * @returns `Product[]` 数组
   */
  listProducts(): Observable<Product[]> {
    return this.unwrap(
      this.http.get<ApiEnvelope<Product[] | PageResult<Product>>>('/api/v1/admin/products')
    ).pipe(map((res) => (Array.isArray(res) ? res : res?.list ?? [])));
  }

  /**
   * 获取单个产品的聚合配置档案（主配置、模块表、范围规则、权限档案、产品知识）。
   * @param id 产品主键 ID
   * @returns `ProductFullBundle` 聚合对象
   */
  getProductBundle(id: number): Observable<ProductFullBundle> {
    return this.unwrap(
      this.http.get<ApiEnvelope<ProductFullBundle>>(`/api/v1/admin/products/${id}`)
    );
  }

  /**
   * 新建产品主档案。
   * @param payload 产品档案表单字段
   * @returns 新建成功的 `Product`
   */
  createProduct(payload: Partial<Product>): Observable<Product> {
    return this.unwrap(this.http.post<ApiEnvelope<Product>>('/api/v1/admin/products', payload));
  }

  /**
   * 更新产品主档案（仓库、构建测试检查命令、预览配方、发布配方与开发规范）。
   * @param id 产品主键 ID
   * @param payload 更新后的产品字段
   * @returns 更新后的 `Product`
   */
  updateProduct(id: number, payload: Partial<Product>): Observable<Product> {
    return this.unwrap(
      this.http.put<ApiEnvelope<Product>>(`/api/v1/admin/products/${id}`, payload)
    );
  }

  /**
   * 删除指定产品档案。
   * @param id 产品主键 ID
   * @returns 删除结果响应
   */
  deleteProduct(id: number): Observable<Record<string, any>> {
    return this.unwrap(
      this.http.delete<ApiEnvelope<Record<string, any>>>(`/api/v1/admin/products/${id}`)
    );
  }

  /**
   * 批量保存产品的业务模块划分与巡检路由配置。
   * @param id 产品主键 ID
   * @param modules 模块定义数组
   * @returns 更新后的 `ProductFullBundle`
   */
  saveProductModules(id: number, modules: ProductModule[]): Observable<ProductFullBundle> {
    return this.unwrap(
      this.http.put<ApiEnvelope<ProductFullBundle>>(`/api/v1/admin/products/${id}/modules`, {
        modules
      })
    );
  }

  /**
   * 批量保存产品的代码改动范围规则（公共区与受保护路径）。
   * @param id 产品主键 ID
   * @param rules 范围规则列表
   * @returns 更新后的 `ProductFullBundle`
   */
  saveProductScopeRules(id: number, rules: ProductScopeRule[]): Observable<ProductFullBundle> {
    return this.unwrap(
      this.http.put<ApiEnvelope<ProductFullBundle>>(`/api/v1/admin/products/${id}/scope-rules`, {
        rules
      })
    );
  }

  /**
   * 更新产品的开发 Agent 沙箱权限档案（关闭 / 默认保护 / 自定义）。
   * @param id 产品主键 ID
   * @param payload 隔离档位及允许写入、额外禁止读取路径列表
   * @returns 更新后的 `ProductPermissionProfile`
   */
  updateProductPermissionProfile(
    id: number,
    payload: {
      mode: string;
      allowedWritePaths: string[];
      extraDeniedReadPaths: string[];
    }
  ): Observable<ProductPermissionProfile> {
    return this.unwrap(
      this.http.put<ApiEnvelope<ProductPermissionProfile>>(
        `/api/v1/admin/products/${id}/permission-profile`,
        payload
      )
    );
  }

  /**
   * 一键将开发 Agent 被权限档案拦截的路径加入该产品的允许写入白名单。
   * @param id 产品主键 ID
   * @param path 被拦截的具体文件系统路径
   * @returns 更新后的 `ProductPermissionProfile`
   */
  allowProductBlockedPath(id: number, path: string): Observable<ProductPermissionProfile> {
    return this.unwrap(
      this.http.post<ApiEnvelope<ProductPermissionProfile>>(
        `/api/v1/admin/products/${id}/permission-profile/allow-path`,
        { path }
      )
    );
  }

  /**
   * 批量保存产品领域知识库条目（供客服 AI 解答咨询与生成低保真原型）。
   * @param id 产品主键 ID
   * @param items 知识条目数组
   * @returns 更新后的 `ProductFullBundle`
   */
  saveProductKnowledge(id: number, items: ProductKnowledge[]): Observable<ProductFullBundle> {
    return this.unwrap(
      this.http.put<ApiEnvelope<ProductFullBundle>>(`/api/v1/admin/products/${id}/knowledge`, {
        items
      })
    );
  }

  /**
   * 获取全部外部 Open API 接入方列表。
   * @returns `IntegrationItem[]` 数组
   */
  listIntegrations(): Observable<IntegrationItem[]> {
    return this.unwrap(
      this.http.get<ApiEnvelope<IntegrationItem[] | PageResult<IntegrationItem>>>(
        '/api/v1/admin/integrations'
      )
    ).pipe(map((res) => (Array.isArray(res) ? res : res?.list ?? [])));
  }

  /**
   * 创建新的业务接入方，获取仅本次一次性明文返回的 `plainAppSecret`。
   * @param payload 接入方名称、IP 白名单、限流配额与绑定产品 ID 数组
   * @returns 包含新建 `integration` 与一次性明文密钥 `plainAppSecret` 的结果
   */
  createIntegration(payload: {
    name: string;
    allowedIpsCsv: string;
    rateLimitRpm: number;
    enabled: boolean;
    productIds: number[];
  }): Observable<{ integration: IntegrationItem; plainAppSecret: string }> {
    return this.unwrap(
      this.http.post<ApiEnvelope<any>>('/api/v1/admin/integrations', payload)
    ).pipe(
      map((res) => {
        if (res?.integration) {
          return {
            integration: res.integration as IntegrationItem,
            plainAppSecret: String(res.plainAppSecret || '')
          };
        }
        return {
          integration: res as IntegrationItem,
          plainAppSecret: String(res?.plainAppSecret || '')
        };
      })
    );
  }

  /**
   * 更新已有接入方配置（来源 IP 白名单、速率限制、启用开关与授权产品）。
   * @param id 接入方主键 ID
   * @param payload 修改后的接入方配置
   * @returns 更新后的 `IntegrationItem`
   */
  updateIntegration(
    id: number,
    payload: {
      name: string;
      allowedIpsCsv: string;
      rateLimitRpm: number;
      enabled: boolean;
      productIds: number[];
    }
  ): Observable<IntegrationItem> {
    return this.unwrap(
      this.http.put<ApiEnvelope<IntegrationItem>>(`/api/v1/admin/integrations/${id}`, payload)
    );
  }

  /**
   * 删除指定业务接入方。
   * @param id 接入方主键 ID
   * @returns 删除结果响应
   */
  deleteIntegration(id: number): Observable<Record<string, any>> {
    return this.unwrap(
      this.http.delete<ApiEnvelope<Record<string, any>>>(`/api/v1/admin/integrations/${id}`)
    );
  }

  /**
   * 获取内嵌的《autoShip 对外开放接口对接开发指南》完整 Markdown 文档文本。
   * @returns 返回包含 `markdown` 字符串的对象流；供设置页在线预览抽屉调用
   */
  getOpenApiDoc(): Observable<{ markdown: string }> {
    return this.unwrap(
      this.http.get<ApiEnvelope<{ markdown: string }>>('/api/v1/admin/integrations/open-api-doc')
    );
  }

  /**
   * 查询所有已注册的开发 Agent 与更新 Agent 列表。
   * @returns `AgentNode[]` 数组
   */
  listAgents(): Observable<AgentNode[]> {
    return this.unwrap(
      this.http.get<ApiEnvelope<AgentNode[] | PageResult<AgentNode>>>('/api/v1/admin/agents')
    ).pipe(map((res) => (Array.isArray(res) ? res : res?.list ?? [])));
  }

  /**
   * 生成一次性 Agent 注册令牌（有效期 15 分钟，供 Agent 首次启动登记 Ed25519 公钥）。
   * @param payload Agent 名称、角色类型（dev / update）、绑定的部署目标 ID 及来源 IP 白名单
   * @returns 包含一次性令牌 `registrationToken` 与过期时间的 `AgentEnrollTokenResult`
   */
  createAgentToken(payload: {
    name: string;
    agentType: string;
    deployTargetId?: number;
    allowedIpsCsv?: string;
  }): Observable<AgentEnrollTokenResult> {
    return this.unwrap(
      this.http.post<ApiEnvelope<AgentEnrollTokenResult>>('/api/v1/admin/agents/tokens', payload)
    );
  }

  /**
   * 立即吊销指定 Agent 的身份凭据，阻断其后续签名请求。
   * @param id Agent 主键 ID
   * @returns 吊销结果响应
   */
  revokeAgent(id: number): Observable<Record<string, any>> {
    return this.unwrap(
      this.http.post<ApiEnvelope<Record<string, any>>>(`/api/v1/admin/agents/${id}/revoke`, {})
    );
  }

  /**
   * 获取系统全局设置（模型通道中继/直连切换、飞书通知 Webhook、自建根 CA 指纹与制品保留数）。
   * @returns `SystemSettingsView` 配置视图
   */
  getSystemSettings(): Observable<SystemSettingsView> {
    return this.unwrap(
      this.http.get<ApiEnvelope<SystemSettingsView>>('/api/v1/admin/settings')
    );
  }

  /**
   * 更新系统全局设置。
   * @param payload 模型通道配置、飞书通知开关与地址、制品保留数量
   * @returns 保存后的 `SystemSettingsView`
   */
  updateSystemSettings(payload: UpdateSystemSettingsPayload): Observable<SystemSettingsView> {
    return this.unwrap(
      this.http.put<ApiEnvelope<SystemSettingsView>>('/api/v1/admin/settings', payload)
    );
  }

  /**
   * 分页检索平台全量不可篡改安全审计日志。
   * @param filter 按模块编码、动作、操作人及分页条件筛选
   * @returns `PageResult<AuditLogItem>` 分页结果
   */
  listAuditLogs(filter: {
    module?: string;
    action?: string;
    actor?: string;
    page?: number;
    pageSize?: number;
  }): Observable<PageResult<AuditLogItem>> {
    return this.unwrap(
      this.http.get<ApiEnvelope<PageResult<AuditLogItem>>>('/api/v1/admin/audit-logs', {
        params: this.buildParams(filter)
      })
    ).pipe(map((res) => this.normalizePage(res)));
  }
}

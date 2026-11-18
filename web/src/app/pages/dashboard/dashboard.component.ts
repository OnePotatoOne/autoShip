import { CommonModule } from '@angular/common';
import { Component, OnInit, computed, inject, signal } from '@angular/core';
import { Router } from '@angular/router';
import { NzAlertModule } from 'ng-zorro-antd/alert';
import { NzBadgeModule } from 'ng-zorro-antd/badge';
import { NzButtonModule } from 'ng-zorro-antd/button';
import { NzEmptyModule } from 'ng-zorro-antd/empty';
import { NzIconModule } from 'ng-zorro-antd/icon';
import { NzMessageService } from 'ng-zorro-antd/message';
import { NzSpinModule } from 'ng-zorro-antd/spin';
import { NzTableModule } from 'ng-zorro-antd/table';
import { NzTagModule } from 'ng-zorro-antd/tag';
import { OverviewData, ProductOverviewCount } from '../../core/models/api.models';
import { AdminApiService } from '../../core/services/admin-api.service';
import {
  formatBytes,
  formatDateTime,
  formatDuration,
  getDeploymentStatusMeta,
  getReleaseStatusMeta,
  getWorkStatusMeta
} from '../../core/utils/format.utils';

/**
 * autoShip 管理后台总览页组件（`/dashboard`）。
 * 对应设计文档第 5.2、5.4 节：展示核心闭环待办计数、各产品进度分布（原生轻量 SVG 条形图）、
 * LLM 模型中继/直连健康度、开发/更新 Agent 实时心跳矩阵、最近发布部署与近期开发任务 Token 耗时统计。
 */
@Component({
  selector: 'app-dashboard',
  standalone: true,
  imports: [
    CommonModule,
    NzSpinModule,
    NzTagModule,
    NzBadgeModule,
    NzButtonModule,
    NzIconModule,
    NzTableModule,
    NzAlertModule,
    NzEmptyModule
  ],
  template: `
    <div class="as-page-header">
      <div>
        <h1 class="as-page-title">
          <span nz-icon nzType="dashboard"></span>
          <span>平台研发交付总览</span>
        </h1>
        <p class="as-page-subtitle">
          从用户问题澄清、自研编码循环、签名发版到受控升级回滚的全链路运行态势
        </p>
      </div>
      <div style="display: flex; gap: 10px;">
        <button nz-button nzType="default" (click)="loadOverview()">
          <span nz-icon nzType="reload" [nzSpin]="loading()"></span>
          <span>刷新态势</span>
        </button>
        <button nz-button nzType="primary" (click)="goTo('/requirements')">
          <span nz-icon nzType="plus"></span>
          <span>前往需求池</span>
        </button>
      </div>
    </div>

    <nz-spin [nzSpinning]="loading()" nzTip="正在同步平台实时运行指标...">
      @if (!overview()?.llmStatus?.healthy && overview()) {
        <nz-alert
          nzType="warning"
          nzShowIcon
          nzMessage="模型通道当前处于离线降级模式（客服会话自动切换为结构化留言表单，恢复后按 FIFO 生成追问）"
          style="margin-bottom: 16px;"
        ></nz-alert>
      }

      <!-- 顶部 5 大核心指标卡片 -->
      <div class="as-kpi-grid">
        <div class="as-kpi-card" (click)="goTo('/requirements')">
          <div class="as-kpi-label">
            <span>待处理需求</span>
            <nz-tag nzColor="blue">需求池</nz-tag>
          </div>
          <div class="as-kpi-value">{{ overview()?.counts?.pendingRequirements ?? 0 }}</div>
          <div class="as-kpi-foot">已提交 / 待分拣 / 待排期</div>
        </div>

        <div class="as-kpi-card" (click)="goTo('/work-items')">
          <div class="as-kpi-label">
            <span>开发进行中</span>
            <nz-tag nzColor="processing">编码循环</nz-tag>
          </div>
          <div class="as-kpi-value">{{ overview()?.counts?.inProgressWork ?? 0 }}</div>
          <div class="as-kpi-foot">规格 / 编码 / 门禁 / 评审</div>
        </div>

        <div class="as-kpi-card" (click)="goTo('/work-items', { status: 'pending_acceptance' })">
          <div class="as-kpi-label">
            <span>待人工验收</span>
            <nz-tag nzColor="purple">人工关口 2</nz-tag>
          </div>
          <div class="as-kpi-value" style="color: #722ed1;">
            {{ overview()?.counts?.pendingAcceptance ?? 0 }}
          </div>
          <div class="as-kpi-foot">改动范围审计 + Tailnet 预览已就绪</div>
        </div>

        <div class="as-kpi-card" (click)="goTo('/releases', { status: 'pending_deploy' })">
          <div class="as-kpi-label">
            <span>待确认部署</span>
            <nz-tag nzColor="gold">人工关口 3</nz-tag>
          </div>
          <div class="as-kpi-value" style="color: #d48806;">
            {{ overview()?.counts?.pendingDeploy ?? 0 }}
          </div>
          <div class="as-kpi-foot">制品已签名上传，待确认下发升级</div>
        </div>

        <div class="as-kpi-card" (click)="goTo('/settings')">
          <div class="as-kpi-label">
            <span>纳管产品总数</span>
            <nz-tag nzColor="cyan">多产品平台</nz-tag>
          </div>
          <div class="as-kpi-value">{{ overview()?.counts?.totalProducts ?? 0 }}</div>
          <div class="as-kpi-foot">独立产品档案与沙箱权限隔离</div>
        </div>
      </div>

      <!-- 模型通道状态 + 按产品交付分布（原生 SVG 可视化） -->
      <div class="two-col-grid">
        <div class="as-card">
          <h3 class="as-section-title">
            <span nz-icon nzType="robot"></span>
            <span>模型通道与中继服务健康度</span>
          </h3>
          <div class="llm-panel">
            <div class="llm-row">
              <span class="llm-key">调用路由模式：</span>
              <nz-tag [nzColor]="overview()?.llmStatus?.mode === 'direct' ? 'cyan' : 'geekblue'">
                {{ overview()?.llmStatus?.mode === 'direct' ? 'Server 直连外部接口 (direct)' : 'Tailnet 经 Mac 开发 Agent 中继 (relay)' }}
              </nz-tag>
            </div>
            <div class="llm-row">
              <span class="llm-key">模型可用状态：</span>
              @if (overview()?.llmStatus?.healthy) {
                <nz-badge nzStatus="success" nzText="正常在线（实时对话与自动分拣可用）"></nz-badge>
              } @else {
                <nz-badge nzStatus="warning" nzText="不可用（已启用离线留言降级）"></nz-badge>
              }
            </div>
            <div class="llm-row">
              <span class="llm-key">Mac 中继 Agent：</span>
              @if (overview()?.llmStatus?.relayAgentOnline) {
                <nz-tag nzColor="success">Tailnet 在线</nz-tag>
              } @else {
                <nz-tag nzColor="default">未连接或离线</nz-tag>
              }
            </div>
            <div class="llm-row">
              <span class="llm-key">最近探活时间：</span>
              <span class="as-mono">{{ formatDateTime(overview()?.llmStatus?.lastCheckedAt) }}</span>
            </div>
          </div>
        </div>

        <div class="as-card">
          <h3 class="as-section-title">
            <span nz-icon nzType="cluster"></span>
            <span>各产品需求与上线分布</span>
          </h3>
          @if ((overview()?.counts?.byProduct?.length ?? 0) === 0) {
            <nz-empty nzNotFoundContent="暂无纳管产品统计数据"></nz-empty>
          } @else {
            <div class="prod-bars">
              @for (p of overview()?.counts?.byProduct || []; track p.productId) {
                <div class="prod-bar-item">
                  <div class="prod-bar-head">
                    <span class="prod-name">{{ p.productName || ('产品 #' + p.productId) }}</span>
                    <span class="prod-nums">
                      总计 <b>{{ p.total }}</b> · 进行中 <b style="color:#1677ff">{{ p.inProgress }}</b> · 已上线 <b style="color:#52c41a">{{ p.deployed }}</b>
                    </span>
                  </div>
                  <!-- 纯原生轻量 SVG 分段进度条 -->
                  <svg width="100%" height="10" viewBox="0 0 300 10" preserveAspectRatio="none" class="svg-bar">
                    <rect x="0" y="0" width="300" height="10" rx="5" fill="#f1f5f9"></rect>
                    <rect
                      x="0"
                      y="0"
                      [attr.width]="calcDeployedWidth(p)"
                      height="10"
                      rx="5"
                      fill="#52c41a"
                    ></rect>
                    <rect
                      [attr.x]="calcDeployedWidth(p)"
                      y="0"
                      [attr.width]="calcInProgressWidth(p)"
                      height="10"
                      fill="#1677ff"
                    ></rect>
                  </svg>
                </div>
              }
            </div>
          }
        </div>
      </div>

      <!-- Agent 节点实时心跳状态表 -->
      <div class="as-card">
        <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px;">
          <h3 class="as-section-title" style="margin: 0;">
            <span nz-icon nzType="api"></span>
            <span>开发 Agent 与生产更新 Agent 在线状态</span>
          </h3>
          <button nz-button nzSize="small" (click)="goTo('/settings')">管理 Agent 与注册令牌</button>
        </div>
        <nz-table
          #agentTable
          [nzData]="overview()?.agents || []"
          nzSize="small"
          [nzShowPagination]="false"
          nzNoResult="暂无已注册 Agent，可前往【系统设置 -> Agent 注册】签发一次性注册令牌"
        >
          <thead>
            <tr>
              <th>ID / 名称</th>
              <th>角色类型</th>
              <th>连接状态</th>
              <th>Agent 版本</th>
              <th>当前产品版本</th>
              <th>服务与磁盘/备份占用</th>
              <th>最近心跳时间</th>
            </tr>
          </thead>
          <tbody>
            @for (ag of agentTable.data; track ag.id) {
              <tr>
                <td>
                  <b>#{{ ag.id }}</b> {{ ag.name }}
                </td>
                <td>
                  @if ((ag.agentType || ag.type) === 'dev') {
                    <nz-tag nzColor="geekblue">开发 Agent (Mac)</nz-tag>
                  } @else {
                    <nz-tag nzColor="cyan">更新 Agent (生产节点)</nz-tag>
                  }
                </td>
                <td>
                  @if (ag.status === 'revoked') {
                    <nz-badge nzStatus="error" nzText="已吊销"></nz-badge>
                  } @else if (ag.online) {
                    <nz-badge nzStatus="success" nzText="在线"></nz-badge>
                  } @else {
                    <nz-badge nzStatus="default" nzText="离线"></nz-badge>
                  }
                </td>
                <td class="as-mono">{{ ag.agentVersion || ag.version || '-' }}</td>
                <td class="as-mono">{{ ag.productVersion || '-' }}</td>
                <td>
                  @if (ag.diskFreeBytes || ag.backupUsedBytes) {
                    <span>剩余 {{ formatBytes(ag.diskFreeBytes) }} / 备份 {{ formatBytes(ag.backupUsedBytes) }}</span>
                  } @else {
                    <span>{{ ag.serviceStatus || '-' }}</span>
                  }
                </td>
                <td class="as-mono">{{ formatDateTime(ag.lastHeartbeatAt) }}</td>
              </tr>
            }
          </tbody>
        </nz-table>
      </div>

      <!-- 最近版本发布 & 最近部署记录 -->
      <div class="two-col-grid">
        <div class="as-card">
          <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px;">
            <h3 class="as-section-title" style="margin: 0;">
              <span nz-icon nzType="rocket"></span>
              <span>最近版本发布</span>
            </h3>
            <a (click)="goTo('/releases')">全部版本</a>
          </div>
          <nz-table
            #relTable
            [nzData]="overview()?.recentReleases || []"
            nzSize="small"
            [nzShowPagination]="false"
            nzNoResult="暂无版本发布记录"
          >
            <thead>
              <tr>
                <th>版本号</th>
                <th>状态</th>
                <th>时间</th>
              </tr>
            </thead>
            <tbody>
              @for (r of relTable.data; track r.id) {
                <tr>
                  <td class="as-mono"><b>{{ r.version || r.tagName }}</b></td>
                  <td>
                    <nz-tag [nzColor]="getReleaseStatusMeta(r.status).color">
                      {{ getReleaseStatusMeta(r.status).label }}
                    </nz-tag>
                  </td>
                  <td class="as-mono">{{ formatDateTime(r.publishedAt || r.createdAt) }}</td>
                </tr>
              }
            </tbody>
          </nz-table>
        </div>

        <div class="as-card">
          <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px;">
            <h3 class="as-section-title" style="margin: 0;">
              <span nz-icon nzType="cloud-server"></span>
              <span>最近生产部署</span>
            </h3>
            <a (click)="goTo('/deployments')">全部部署</a>
          </div>
          <nz-table
            #depTable
            [nzData]="overview()?.recentDeployments || []"
            nzSize="small"
            [nzShowPagination]="false"
            nzNoResult="暂无生产部署记录"
          >
            <thead>
              <tr>
                <th>部署 ID / 目标</th>
                <th>版本</th>
                <th>状态</th>
                <th>时间</th>
              </tr>
            </thead>
            <tbody>
              @for (d of depTable.data; track d.id) {
                <tr>
                  <td>#{{ d.id }} {{ d.targetName || ('目标#' + d.targetId) }}</td>
                  <td class="as-mono">{{ d.version || d.targetVersion || '-' }}</td>
                  <td>
                    <nz-tag [nzColor]="getDeploymentStatusMeta(d.status).color">
                      {{ getDeploymentStatusMeta(d.status).label }}
                    </nz-tag>
                  </td>
                  <td class="as-mono">{{ formatDateTime(d.startedAt || d.createdAt) }}</td>
                </tr>
              }
            </tbody>
          </nz-table>
        </div>
      </div>

      <!-- 近期开发任务 Token 用量与耗时统计（5.4 节） -->
      <div class="as-card">
        <h3 class="as-section-title">
          <span nz-icon nzType="thunderbolt"></span>
          <span>近期开发任务 Token 消耗与耗时</span>
        </h3>
        <nz-table
          #usageTable
          [nzData]="overview()?.recentWorkUsage || []"
          nzSize="small"
          [nzShowPagination]="false"
          nzNoResult="暂无开发任务执行用量记录"
        >
          <thead>
            <tr>
              <th>任务 ID</th>
              <th>任务标题</th>
              <th>当前状态</th>
              <th>输入 / 输出 Token</th>
              <th>总 Token 占比</th>
              <th>总耗时</th>
            </tr>
          </thead>
          <tbody>
            @for (u of usageTable.data; track u.workItemId) {
              <tr>
                <td class="as-mono">#{{ u.workItemId }}</td>
                <td>{{ u.title }}</td>
                <td>
                  <nz-tag [nzColor]="getWorkStatusMeta(u.status).color">
                    {{ getWorkStatusMeta(u.status).label }}
                  </nz-tag>
                </td>
                <td class="as-mono">
                  {{ u.promptTokens | number }} / {{ u.completionTokens | number }}
                </td>
                <td style="min-width: 170px;">
                  <div style="display: flex; align-items: center; gap: 8px;">
                    <svg width="90" height="8" viewBox="0 0 90 8">
                      <rect x="0" y="0" width="90" height="8" rx="4" fill="#e2e8f0"></rect>
                      <rect
                        x="0"
                        y="0"
                        [attr.width]="calcTokenWidth(u.totalTokens)"
                        height="8"
                        rx="4"
                        fill="#1677ff"
                      ></rect>
                    </svg>
                    <span class="as-mono">{{ u.totalTokens | number }}</span>
                  </div>
                </td>
                <td class="as-mono">{{ formatDuration(u.durationSeconds) }}</td>
              </tr>
            }
          </tbody>
        </nz-table>
      </div>
    </nz-spin>
  `,
  styles: [
    `
      .two-col-grid {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(400px, 1fr));
        gap: 16px;
      }
      .llm-panel {
        display: flex;
        flex-direction: column;
        gap: 12px;
      }
      .llm-row {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 12px;
        border-radius: 8px;
        background: #f8fafc;
        font-size: 13.5px;
      }
      .llm-key {
        color: #475569;
        font-weight: 500;
      }
      .prod-bars {
        display: flex;
        flex-direction: column;
        gap: 12px;
      }
      .prod-bar-head {
        display: flex;
        justify-content: space-between;
        font-size: 13px;
        margin-bottom: 5px;
      }
      .prod-name {
        font-weight: 600;
        color: #0f172a;
      }
      .prod-nums {
        color: #64748b;
        font-size: 12px;
      }
      .svg-bar {
        display: block;
        border-radius: 5px;
        overflow: hidden;
      }
    `
  ]
})
export class DashboardComponent implements OnInit {
  private readonly api = inject(AdminApiService);
  private readonly router = inject(Router);
  private readonly message = inject(NzMessageService);

  readonly loading = signal<boolean>(false);
  readonly overview = signal<OverviewData | null>(null);

  /** 计算近期任务中最大的 Token 用量，用于归一化 SVG 条形图长度 */
  readonly maxUsageTokens = computed<number>(() => {
    const list = this.overview()?.recentWorkUsage || [];
    let maxVal = 1;
    for (const item of list) {
      if ((item.totalTokens || 0) > maxVal) {
        maxVal = item.totalTokens;
      }
    }
    return maxVal;
  });

  readonly formatBytes = formatBytes;
  readonly formatDateTime = formatDateTime;
  readonly formatDuration = formatDuration;
  readonly getReleaseStatusMeta = getReleaseStatusMeta;
  readonly getDeploymentStatusMeta = getDeploymentStatusMeta;
  readonly getWorkStatusMeta = getWorkStatusMeta;

  /**
   * 页面初始化时加载总览数据。
   * @returns void
   */
  ngOnInit(): void {
    this.loadOverview();
  }

  /**
   * 请求 `/api/v1/admin/overview` 获取最新统计指标。
   * @returns void
   */
  loadOverview(): void {
    this.loading.set(true);
    this.api.getOverview().subscribe({
      next: (data) => {
        this.loading.set(false);
        this.overview.set(data);
      },
      error: (err: Error) => {
        this.loading.set(false);
        this.message.error(err.message || '加载总览数据失败');
      }
    });
  }

  /**
   * 跳转到对应的业务工作台路由。
   * @param path 目标路由路径
   * @param queryParams 可选查询过滤参数
   * @returns void
   */
  goTo(path: string, queryParams?: Record<string, string>): void {
    void this.router.navigate([path], { queryParams });
  }

  /**
   * 计算产品已上线需求在 300 宽度 SVG 进度条中的像素宽度。
   * @param item 产品聚合统计对象
   * @returns 像素宽度数值（0 ~ 300）
   */
  calcDeployedWidth(item: ProductOverviewCount): number {
    const total = Math.max(item.total || 0, 1);
    return Math.min(300, Math.round(((item.deployed || 0) / total) * 300));
  }

  /**
   * 计算产品进行中需求在 300 宽度 SVG 进度条中的像素宽度。
   * @param item 产品聚合统计对象
   * @returns 像素宽度数值（0 ~ 300）
   */
  calcInProgressWidth(item: ProductOverviewCount): number {
    const total = Math.max(item.total || 0, 1);
    return Math.min(300, Math.round(((item.inProgress || 0) / total) * 300));
  }

  /**
   * 计算某任务 Token 消耗相对于最大任务在 90px SVG 条中的宽度。
   * @param tokens 当前任务总 Token
   * @returns 像素宽度（4 ~ 90）
   */
  calcTokenWidth(tokens: number): number {
    if (!tokens || tokens <= 0) {
      return 0;
    }
    return Math.max(4, Math.round((tokens / this.maxUsageTokens()) * 90));
  }
}

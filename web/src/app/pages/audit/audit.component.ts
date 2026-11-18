import { CommonModule } from '@angular/common';
import { Component, OnInit, inject, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { Router } from '@angular/router';
import { NzButtonModule } from 'ng-zorro-antd/button';
import { NzDescriptionsModule } from 'ng-zorro-antd/descriptions';
import { NzIconModule } from 'ng-zorro-antd/icon';
import { NzInputModule } from 'ng-zorro-antd/input';
import { NzMessageService } from 'ng-zorro-antd/message';
import { NzModalModule } from 'ng-zorro-antd/modal';
import { NzSelectModule } from 'ng-zorro-antd/select';
import { NzTableModule } from 'ng-zorro-antd/table';
import { NzTagModule } from 'ng-zorro-antd/tag';
import { AuditLogItem } from '../../core/models/api.models';
import { AdminApiService } from '../../core/services/admin-api.service';
import { formatDateTime } from '../../core/utils/format.utils';

/**
 * autoShip 管理后台全量安全与操作审计页面组件（`/audit`）。
 * 对应设计文档第 5.1、5.2、10 节：检索管理后台操作、Agent 指令下发、方案/验收审批、版本发布与生产部署的全量脱敏审计轨迹。
 */
@Component({
  selector: 'app-audit',
  standalone: true,
  imports: [
    CommonModule,
    FormsModule,
    NzTableModule,
    NzButtonModule,
    NzInputModule,
    NzSelectModule,
    NzTagModule,
    NzIconModule,
    NzModalModule,
    NzDescriptionsModule
  ],
  template: `
    <div class="as-page-header">
      <div>
        <h1 class="as-page-title">
          <span nz-icon nzType="audit"></span>
          <span>平台全量安全与操作审计</span>
        </h1>
        <p class="as-page-subtitle">
          不可篡改记录后台审批、指令投递、Open API 调用及生产升级操作；所有敏感凭据与堆栈均已脱敏入库
        </p>
      </div>
      <div style="display: flex; gap: 10px;">
        <button nz-button nzType="default" (click)="goToSettings()">
          <span nz-icon nzType="setting"></span>
          <span>前往系统设置</span>
        </button>
        <button nz-button nzType="default" (click)="loadLogs()">
          <span nz-icon nzType="reload" [nzSpin]="loading()"></span>
          <span>刷新日志</span>
        </button>
      </div>
    </div>

    <!-- 审计筛选栏 -->
    <div class="as-card">
      <div class="as-filter-bar" style="margin-bottom: 0;">
        <nz-select
          style="width: 180px;"
          nzPlaceHolder="按业务模块筛选"
          nzAllowClear
          [(ngModel)]="filterModule"
          (ngModelChange)="searchFromFirstPage()"
        >
          <nz-option nzValue="auth" nzLabel="认证鉴权 (auth)"></nz-option>
          <nz-option nzValue="product" nzLabel="产品档案 (product)"></nz-option>
          <nz-option nzValue="integration" nzLabel="接入方 (integration)"></nz-option>
          <nz-option nzValue="requirement" nzLabel="需求池 (requirement)"></nz-option>
          <nz-option nzValue="work" nzLabel="开发任务 (work)"></nz-option>
          <nz-option nzValue="release" nzLabel="版本管理 (release)"></nz-option>
          <nz-option nzValue="deploy" nzLabel="部署中心 (deploy)"></nz-option>
          <nz-option nzValue="agent" nzLabel="Agent 管理 (agent)"></nz-option>
          <nz-option nzValue="settings" nzLabel="全局设置 (settings)"></nz-option>
        </nz-select>

        <input
          nz-input
          style="width: 190px;"
          placeholder="执行动作 (如 confirm_deploy)"
          [(ngModel)]="filterAction"
          (keyup.enter)="searchFromFirstPage()"
        />

        <input
          nz-input
          style="width: 190px;"
          placeholder="操作主体标识 (如 admin)"
          [(ngModel)]="filterActor"
          (keyup.enter)="searchFromFirstPage()"
        />

        <button nz-button nzType="primary" (click)="searchFromFirstPage()">
          <span nz-icon nzType="search"></span>检索
        </button>
        <button nz-button nzType="default" (click)="resetFilters()">重置</button>
      </div>
    </div>

    <!-- 审计日志数据表 -->
    <div class="as-card">
      <nz-table
        #auditTable
        [nzData]="items()"
        [nzLoading]="loading()"
        [nzFrontPagination]="false"
        [nzTotal]="total()"
        [nzPageIndex]="page()"
        [nzPageSize]="pageSize()"
        (nzPageIndexChange)="onPageChange($event)"
        (nzPageSizeChange)="onPageSizeChange($event)"
        nzShowSizeChanger
        nzSize="small"
      >
        <thead>
          <tr>
            <th nzWidth="80px">ID</th>
            <th nzWidth="160px">发生时间</th>
            <th nzWidth="170px">操作主体</th>
            <th nzWidth="120px">所属模块</th>
            <th nzWidth="165px">动作标识</th>
            <th>请求方法 / 路径 / 目标对象</th>
            <th nzWidth="125px">来源 IP</th>
            <th nzWidth="90px">状态码</th>
            <th nzWidth="85px">详情</th>
          </tr>
        </thead>
        <tbody>
          @for (item of auditTable.data; track item.id) {
            <tr>
              <td class="as-mono">#{{ item.id }}</td>
              <td class="as-mono">{{ formatDateTime(item.createdAt) }}</td>
              <td>
                <nz-tag [nzColor]="getActorColor(item.actorType)">
                  {{ item.actorType || 'admin' }}
                </nz-tag>
                <span class="as-mono">{{ item.actorId }}</span>
              </td>
              <td><span class="as-mono">{{ item.module }}</span></td>
              <td><b class="as-mono">{{ item.action }}</b></td>
              <td>
                <div class="as-mono" style="font-size: 12px;">
                  @if (item.method) {
                    <nz-tag>{{ item.method }}</nz-tag>
                  }
                  <span>{{ item.path || '-' }}</span>
                </div>
                @if (item.targetType || item.targetId) {
                  <div style="font-size: 11.5px; color: #64748b;">
                    目标: {{ item.targetType }} #{{ item.targetId }}
                  </div>
                }
                @if (item.errorMsg) {
                  <div style="font-size: 11.5px; color: #cf1322;">{{ item.errorMsg }}</div>
                }
              </td>
              <td class="as-mono">{{ item.clientIp || '-' }}</td>
              <td>
                <nz-tag [nzColor]="item.statusCode >= 400 ? 'error' : 'success'">
                  {{ item.statusCode || 200 }}
                </nz-tag>
              </td>
              <td>
                <button nz-button nzSize="small" (click)="openDetailModal(item)">查看</button>
              </td>
            </tr>
          }
        </tbody>
      </nz-table>
    </div>

    <!-- 审计记录完整脱敏 JSON 详情弹窗 -->
    <nz-modal
      [nzVisible]="detailModalVisible()"
      [nzTitle]="'审计记录详情 #' + (selectedItem()?.id || '')"
      [nzWidth]="720"
      [nzFooter]="null"
      (nzOnCancel)="detailModalVisible.set(false)"
    >
      <ng-container *nzModalContent>
        @if (selectedItem(); as log) {
          <nz-descriptions nzBordered [nzColumn]="2" nzSize="small" style="margin-bottom: 14px;">
            <nz-descriptions-item nzTitle="操作主体">
              {{ log.actorType }} / {{ log.actorId }}
            </nz-descriptions-item>
            <nz-descriptions-item nzTitle="发生时间">
              {{ formatDateTime(log.createdAt) }}
            </nz-descriptions-item>
            <nz-descriptions-item nzTitle="模块与动作">
              {{ log.module }} :: {{ log.action }}
            </nz-descriptions-item>
            <nz-descriptions-item nzTitle="来源 IP 与状态码">
              {{ log.clientIp }} (HTTP {{ log.statusCode }})
            </nz-descriptions-item>
            <nz-descriptions-item nzTitle="请求路径" [nzSpan]="2">
              <span class="as-mono">{{ log.method }} {{ log.path }}</span>
            </nz-descriptions-item>
          </nz-descriptions>

          <div style="font-weight: 600; margin-bottom: 6px;">脱敏结构化载荷 (detailJson)</div>
          <pre class="as-terminal">{{ formatPrettyJson(log.detailJson) }}</pre>
        }
      </ng-container>
    </nz-modal>
  `
})
export class AuditComponent implements OnInit {
  private readonly api = inject(AdminApiService);
  private readonly router = inject(Router);
  private readonly message = inject(NzMessageService);

  readonly items = signal<AuditLogItem[]>([]);
  readonly total = signal<number>(0);
  readonly page = signal<number>(1);
  readonly pageSize = signal<number>(20);
  readonly loading = signal<boolean>(false);

  filterModule: string | null = null;
  filterAction = '';
  filterActor = '';

  readonly detailModalVisible = signal<boolean>(false);
  readonly selectedItem = signal<AuditLogItem | null>(null);

  readonly formatDateTime = formatDateTime;

  /**
   * 组件初始化时加载第一页审计日志。
   * @returns void
   */
  ngOnInit(): void {
    this.loadLogs();
  }

  /**
   * 请求 `/api/v1/admin/audit-logs` 获取分页审计日志。
   * @returns void
   */
  loadLogs(): void {
    this.loading.set(true);
    this.api
      .listAuditLogs({
        module: this.filterModule ?? undefined,
        action: this.filterAction,
        actor: this.filterActor,
        page: this.page(),
        pageSize: this.pageSize()
      })
      .subscribe({
        next: (res) => {
          this.loading.set(false);
          this.items.set(res.list);
          this.total.set(res.total);
        },
        error: (err: Error) => {
          this.loading.set(false);
          this.message.error(err.message || '加载审计日志失败');
        }
      });
  }

  /**
   * 从第 1 页执行检索。
   * @returns void
   */
  searchFromFirstPage(): void {
    this.page.set(1);
    this.loadLogs();
  }

  /**
   * 清空筛选条件。
   * @returns void
   */
  resetFilters(): void {
    this.filterModule = null;
    this.filterAction = '';
    this.filterActor = '';
    this.page.set(1);
    this.loadLogs();
  }

  /**
   * 页码改变事件处理。
   * @param idx 页码
   * @returns void
   */
  onPageChange(idx: number): void {
    this.page.set(idx);
    this.loadLogs();
  }

  /**
   * 每页条数改变事件处理。
   * @param size 每页条数
   * @returns void
   */
  onPageSizeChange(size: number): void {
    this.pageSize.set(size);
    this.page.set(1);
    this.loadLogs();
  }

  /**
   * 根据操作主体类型映射徽标颜色。
   * @param actorType 主体类型字符串
   * @returns 颜色名称
   */
  getActorColor(actorType?: string): string {
    switch (actorType) {
      case 'admin':
        return 'geekblue';
      case 'dev_agent':
        return 'purple';
      case 'update_agent':
        return 'cyan';
      case 'open_api':
        return 'gold';
      default:
        return 'default';
    }
  }

  /**
   * 打开单条审计日志的详情弹窗。
   * @param item 选中的审计日志
   * @returns void
   */
  openDetailModal(item: AuditLogItem): void {
    this.selectedItem.set(item);
    this.detailModalVisible.set(true);
  }

  /**
   * 美化输出脱敏 JSON 字符串。
   * @param raw 原始 JSON 文本
   * @returns 缩进后的 JSON 字符串
   */
  formatPrettyJson(raw?: string): string {
    if (!raw) {
      return '{}';
    }
    try {
      return JSON.stringify(JSON.parse(raw), null, 2);
    } catch {
      return raw;
    }
  }

  /**
   * 跳转到系统设置页。
   * @returns void
   */
  goToSettings(): void {
    void this.router.navigate(['/settings']);
  }
}

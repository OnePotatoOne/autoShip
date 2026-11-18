import { CommonModule } from '@angular/common';
import { Component, OnInit, computed, inject, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { ActivatedRoute, Router } from '@angular/router';
import { NzAlertModule } from 'ng-zorro-antd/alert';
import { NzBadgeModule } from 'ng-zorro-antd/badge';
import { NzButtonModule } from 'ng-zorro-antd/button';
import { NzCheckboxModule } from 'ng-zorro-antd/checkbox';
import { NzDescriptionsModule } from 'ng-zorro-antd/descriptions';
import { NzDividerModule } from 'ng-zorro-antd/divider';
import { NzDrawerModule } from 'ng-zorro-antd/drawer';
import { NzEmptyModule } from 'ng-zorro-antd/empty';
import { NzFormModule } from 'ng-zorro-antd/form';
import { NzIconModule } from 'ng-zorro-antd/icon';
import { NzInputModule } from 'ng-zorro-antd/input';
import { NzMessageService } from 'ng-zorro-antd/message';
import { NzModalModule } from 'ng-zorro-antd/modal';
import { NzSelectModule } from 'ng-zorro-antd/select';
import { NzSpinModule } from 'ng-zorro-antd/spin';
import { NzTableModule } from 'ng-zorro-antd/table';
import { NzTabsModule } from 'ng-zorro-antd/tabs';
import { NzTagModule } from 'ng-zorro-antd/tag';
import {
  DeployTarget,
  Product,
  ReleaseDetailBundle,
  ReleaseItem
} from '../../core/models/api.models';
import { AdminApiService } from '../../core/services/admin-api.service';
import {
  formatBytes,
  formatDateTime,
  getDeploymentStatusMeta,
  getMigrationRiskMeta,
  getReleaseStatusMeta
} from '../../core/utils/format.utils';

/**
 * autoShip 管理后台版本管理与确认部署关口组件（`/releases`）。
 * 对应设计文档第 4.1、5.2、8.10、9.5 节：
 * 1. 展示版本变更日志、Ed25519 签名制品清单、CI 运行链接与历史部署；
 * 2. 当版本处于 `publish_failed` 时提供醒目的“新建关联修复任务”按钮；
 * 3. “确认部署”弹窗完整呈现变更日志、包含需求、迁移风险等级徽标、备份存储超限告警、手动勾选先备份数据库与各目标当前版本对比。
 */
@Component({
  selector: 'app-releases',
  standalone: true,
  imports: [
    CommonModule,
    FormsModule,
    NzTableModule,
    NzButtonModule,
    NzSelectModule,
    NzInputModule,
    NzTagModule,
    NzBadgeModule,
    NzIconModule,
    NzDrawerModule,
    NzModalModule,
    NzFormModule,
    NzTabsModule,
    NzDescriptionsModule,
    NzAlertModule,
    NzEmptyModule,
    NzSpinModule,
    NzCheckboxModule,
    NzDividerModule
  ],
  template: `
    <div class="as-page-header">
      <div>
        <h1 class="as-page-title">
          <span nz-icon nzType="rocket"></span>
          <span>版本与签名制品管理</span>
        </h1>
        <p class="as-page-subtitle">
          Ed25519 签名制品库；人工关口 3：核对变更日志、迁移风险等级与目标当前版本后确认下发生产部署
        </p>
      </div>
      <div>
        <button nz-button nzType="default" (click)="loadList()">
          <span nz-icon nzType="reload" [nzSpin]="loading()"></span>
          <span>刷新版本列表</span>
        </button>
      </div>
    </div>

    <!-- 筛选条件栏 -->
    <div class="as-card">
      <div class="as-filter-bar" style="margin-bottom: 0;">
        <nz-select
          style="width: 200px;"
          nzPlaceHolder="按产品筛选"
          nzAllowClear
          [(ngModel)]="filterProductId"
          (ngModelChange)="searchFromFirstPage()"
        >
          @for (p of products(); track p.id) {
            <nz-option [nzValue]="p.id" [nzLabel]="p.name + ' (' + p.code + ')'"></nz-option>
          }
        </nz-select>

        <nz-select
          style="width: 185px;"
          nzPlaceHolder="版本状态"
          nzAllowClear
          [(ngModel)]="filterStatus"
          (ngModelChange)="searchFromFirstPage()"
        >
          <nz-option nzValue="publishing" nzLabel="发布中 (publishing)"></nz-option>
          <nz-option nzValue="publish_failed" nzLabel="发布失败 (publish_failed)"></nz-option>
          <nz-option nzValue="pending_deploy" nzLabel="待部署 (pending_deploy)"></nz-option>
          <nz-option nzValue="deployed" nzLabel="已部署 (deployed)"></nz-option>
          <nz-option nzValue="superseded" nzLabel="已取代 (superseded)"></nz-option>
        </nz-select>

        <button nz-button nzType="primary" (click)="searchFromFirstPage()">筛选</button>
        <button nz-button nzType="default" (click)="resetFilters()">重置</button>
      </div>
    </div>

    <!-- 版本列表表格 -->
    <div class="as-card">
      <nz-table
        #relTable
        [nzData]="items()"
        [nzLoading]="loading()"
        [nzFrontPagination]="false"
        [nzTotal]="total()"
        [nzPageIndex]="page()"
        [nzPageSize]="pageSize()"
        (nzPageIndexChange)="onPageChange($event)"
        (nzPageSizeChange)="onPageSizeChange($event)"
        nzShowSizeChanger
        nzSize="middle"
      >
        <thead>
          <tr>
            <th nzWidth="76px">ID</th>
            <th nzWidth="150px">所属产品</th>
            <th nzWidth="140px">版本号 / Tag</th>
            <th nzWidth="125px">Commit SHA</th>
            <th nzWidth="165px">迁移风险分级</th>
            <th nzWidth="125px">版本状态</th>
            <th nzWidth="160px">发布/创建时间</th>
            <th nzWidth="270px">操作</th>
          </tr>
        </thead>
        <tbody>
          @for (r of relTable.data; track r.id) {
            <tr>
              <td class="as-mono">#{{ r.id }}</td>
              <td><b>{{ getProductName(r.productId, r.productName) }}</b></td>
              <td>
                <a class="as-mono" style="font-weight: 700; font-size: 13.5px;" (click)="openDetail(r.id)">
                  {{ r.version }}
                </a>
                @if (r.tagName && r.tagName !== r.version) {
                  <div class="as-mono" style="font-size: 11.5px; color: #64748b;">{{ r.tagName }}</div>
                }
              </td>
              <td class="as-mono">{{ r.commitSha ? r.commitSha.slice(0, 10) : '-' }}</td>
              <td>
                <nz-tag [nzColor]="getMigrationRiskMeta(r.migrationRisk).color">
                  {{ getMigrationRiskMeta(r.migrationRisk).label }}
                </nz-tag>
              </td>
              <td>
                <nz-tag [nzColor]="getReleaseStatusMeta(r.status).color">
                  {{ getReleaseStatusMeta(r.status).label }}
                </nz-tag>
              </td>
              <td class="as-mono">{{ formatDateTime(r.publishedAt || r.createdAt) }}</td>
              <td>
                <div style="display: flex; gap: 6px; flex-wrap: wrap;">
                  <button nz-button nzSize="small" (click)="openDetail(r.id)">详情</button>

                  @if (r.status === 'pending_deploy' || r.status === 'deployed') {
                    <button nz-button nzType="primary" nzSize="small" (click)="openConfirmDeployModal(r.id)">
                      <span nz-icon nzType="cloud-server"></span>确认部署
                    </button>
                  }

                  <!-- 核心要求：当版本处于 publish_failed 时，提供醒目的“新建关联修复任务”按钮 -->
                  @if (r.status === 'publish_failed') {
                    <button
                      nz-button
                      nzType="primary"
                      nzDanger
                      nzSize="small"
                      (click)="openCreateFixModal(r)"
                    >
                      <span nz-icon nzType="tool"></span>新建关联修复任务
                    </button>
                  }

                  @if (r['fixWorkItemId']) {
                    <button
                      nz-button
                      nzType="dashed"
                      nzSize="small"
                      (click)="jumpToWorkItem(r['fixWorkItemId'])"
                    >
                      修复单 #{{ r['fixWorkItemId'] }}
                    </button>
                  }
                </div>
              </td>
            </tr>
          }
        </tbody>
      </nz-table>
    </div>

    <!-- 新建关联修复开发任务弹窗 (publish_failed) -->
    <nz-modal
      [nzVisible]="fixModalVisible()"
      nzTitle="针对发布失败版本新建关联修复任务"
      [nzOkLoading]="submitting()"
      nzOkText="立即创建修复任务"
      [nzOkDanger]="true"
      (nzOnCancel)="fixModalVisible.set(false)"
      (nzOnOk)="submitCreateFixWork()"
    >
      <ng-container *nzModalContent>
        <nz-alert
          nzType="warning"
          nzShowIcon
          nzMessage="遵循不可移动已有 Git Tag 规范：关联修复任务将在主分支基础上修复 CI/编译故障，验收后自动以递增的新版本号重新发布。"
          style="margin-bottom: 14px;"
        ></nz-alert>
        @if (activeFixRelease()?.failureReason) {
          <div style="margin-bottom: 12px;">
            <div style="font-size: 12px; color: #cf1322; font-weight: 600; margin-bottom: 4px;">
              原版本发布失败诊断摘要：
            </div>
            <pre class="as-terminal" style="max-height: 150px;">{{ activeFixRelease()?.failureReason }}</pre>
          </div>
        }
        <form nz-form nzLayout="vertical">
          <nz-form-item>
            <nz-form-label nzRequired>修复说明与补充提示</nz-form-label>
            <nz-form-control>
              <textarea
                nz-input
                name="fixNote"
                rows="3"
                [(ngModel)]="fixWorkNote"
                placeholder="补充针对本次 CI 编译或打包失败的修复指示..."
              ></textarea>
            </nz-form-control>
          </nz-form-item>
        </form>
      </ng-container>
    </nz-modal>

    <!-- “确认部署”核心弹窗（人工关口 3） -->
    <nz-modal
      [nzVisible]="deployModalVisible()"
      [nzTitle]="'确认下发生产部署 · ' + (deployBundle()?.release?.version || '')"
      [nzWidth]="840"
      [nzOkLoading]="submitting()"
      nzOkText="确认无误并下发升级指令"
      (nzOnCancel)="deployModalVisible.set(false)"
      (nzOnOk)="submitConfirmDeploy()"
    >
      <ng-container *nzModalContent>
        <nz-spin [nzSpinning]="deployModalLoading()">
          @if (deployBundle(); as db) {
            <!-- 1. 版本号与迁移风险等级徽标 -->
            <div class="deploy-meta-bar">
              <div>
                <span>目标发布版本：</span>
                <b class="as-mono" style="font-size: 16px; color: #1677ff;">{{ db.release.version }}</b>
              </div>
              <div>
                <span>迁移风险等级：</span>
                <nz-tag [nzColor]="getMigrationRiskMeta(db.release.migrationRisk).color">
                  {{ getMigrationRiskMeta(db.release.migrationRisk).label }}
                </nz-tag>
              </div>
              <div>
                <span>Ed25519 清单签名：</span>
                <nz-tag nzColor="success">已签名核验</nz-tag>
              </div>
            </div>

            <!-- 2. 备份存储超限告警提示（9.5 节） -->
            @if (hasStorageExceededWarning()) {
              <nz-alert
                nzType="error"
                nzShowIcon
                nzMessage="备份存储占用超限告警！"
                nzDescription="所选部署目标中存在专用备份目录占用超出配额上限的节点！平台不会自动删除旧备份，请部署后前往【部署中心 -> 数据库备份】清理旧备份或调高配额；若目标服务器实际剩余磁盘不足以写下本次备份，更新 Agent 将安全阻断本次升级。"
                style="margin-bottom: 14px;"
              ></nz-alert>
            }

            <!-- 3. 变更日志与包含的需求 -->
            <div style="display: grid; grid-template-columns: 1.2fr 1fr; gap: 12px; margin-bottom: 14px;">
              <div class="as-subtle-box">
                <div style="font-weight: 600; margin-bottom: 6px;">本版变更日志 (Changelog)</div>
                <div style="white-space: pre-wrap; max-height: 150px; overflow-y: auto; font-size: 13px;">
                  {{ db.release.changelog || '暂无变更日志正文' }}
                </div>
              </div>

              <div class="as-subtle-box">
                <div style="font-weight: 600; margin-bottom: 6px;">
                  包含的需求列表 ({{ db.requirements.length || 0 }})
                </div>
                <div style="max-height: 150px; overflow-y: auto; font-size: 13px;">
                  @for (req of db.requirements; track req.id) {
                    <div style="margin-bottom: 4px;">
                      <span class="as-mono">#{{ req.id }}</span> {{ req.title }}
                    </div>
                  } @empty {
                    <span style="color: #64748b;">无关联需求（手动上传发布或修复版本）</span>
                  }
                </div>
              </div>
            </div>

            <!-- 4. 各部署目标当前版本对比与勾选 -->
            <h4 class="as-section-title">选择要升级的生产部署目标（当前运行版本对比）</h4>
            <nz-table
              #targetTable
              [nzData]="availableTargets()"
              nzSize="small"
              [nzShowPagination]="false"
              nzNoResult="该产品尚未配置生产部署目标，请先前往【部署中心 -> 部署目标管理】新建"
            >
              <thead>
                <tr>
                  <th nzWidth="54px">选择</th>
                  <th>部署目标名称 / 环境</th>
                  <th nzWidth="110px">Agent 状态</th>
                  <th nzWidth="210px">版本升级对比</th>
                  <th nzWidth="210px">备份配额与剩余磁盘</th>
                </tr>
              </thead>
              <tbody>
                @for (t of targetTable.data; track t.id) {
                  <tr>
                    <td>
                      <label
                        nz-checkbox
                        [ngModel]="selectedTargetIds().includes(t.id)"
                        (ngModelChange)="toggleTargetSelection(t.id, $event)"
                      ></label>
                    </td>
                    <td>
                      <b>{{ t.name }}</b>
                      @if (t.env || t.environment) {
                        <nz-tag style="margin-left: 6px;">{{ t.env || t.environment }}</nz-tag>
                      }
                    </td>
                    <td>
                      @if (t.agentOnline) {
                        <nz-badge nzStatus="success" nzText="在线"></nz-badge>
                      } @else {
                        <nz-badge nzStatus="default" nzText="待心跳"></nz-badge>
                      }
                    </td>
                    <td class="as-mono">
                      <span>{{ t.currentVersion || '首次安装' }}</span>
                      <span style="color: #1677ff; margin: 0 6px;">→</span>
                      <b style="color: #389e0d;">{{ db.release.version }}</b>
                    </td>
                    <td style="font-size: 12px;">
                      <div>
                        备份占用: {{ formatTargetBackup(t) }}
                        @if (isTargetStorageExceeded(t)) {
                          <nz-tag nzColor="error" style="margin-left: 4px;">超限告警</nz-tag>
                        }
                      </div>
                      @if (t.diskFreeBytes || t.diskFreeMb) {
                        <div style="color: #64748b;">
                          可用磁盘: {{ t.diskFreeBytes ? formatBytes(t.diskFreeBytes) : (t.diskFreeMb + ' MB') }}
                        </div>
                      }
                    </td>
                  </tr>
                }
              </tbody>
            </nz-table>

            <!-- 5. 手动勾选“先备份数据库”复选框 -->
            <div class="backup-check-box">
              <label nz-checkbox [(ngModel)]="confirmBackupDb">
                <b>升级前先执行数据库备份 (backup-db)</b>
              </label>
              <div style="font-size: 12px; color: #475569; margin-top: 4px; padding-left: 24px;">
                说明：默认不备份数据库；当迁移风险等级为“含数据改写”或手动勾选此项时，更新 Agent 将先调用产品部署钩子导出数据库到专用目录，备份成功才继续升级。
              </div>
            </div>
          }
        </nz-spin>
      </ng-container>
    </nz-modal>

    <!-- 版本详情抽屉 -->
    <nz-drawer
      [nzVisible]="drawerVisible()"
      [nzWidth]="900"
      [nzTitle]="'版本详情 · ' + (detail()?.release?.version || '')"
      (nzOnClose)="drawerVisible.set(false)"
    >
      <ng-container *nzDrawerContent>
        <nz-spin [nzSpinning]="detailLoading()">
          @if (detail()?.release; as rel) {
            <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 14px; flex-wrap: wrap; gap: 8px;">
              <div style="display: flex; gap: 8px; align-items: center;">
                <nz-tag [nzColor]="getReleaseStatusMeta(rel.status).color">
                  {{ getReleaseStatusMeta(rel.status).label }}
                </nz-tag>
                <nz-tag [nzColor]="getMigrationRiskMeta(rel.migrationRisk).color">
                  {{ getMigrationRiskMeta(rel.migrationRisk).label }}
                </nz-tag>
                @if (rel.ciRunUrl) {
                  <a [href]="rel.ciRunUrl" target="_blank" rel="noopener noreferrer">
                    <span nz-icon nzType="link"></span> 查看产品 CI 运行记录
                  </a>
                }
              </div>

              <div style="display: flex; gap: 8px;">
                @if (rel.status === 'publish_failed') {
                  <button nz-button nzType="primary" nzDanger nzSize="small" (click)="openCreateFixModal(rel)">
                    <span nz-icon nzType="tool"></span>新建关联修复任务
                  </button>
                }
                @if (rel.status === 'pending_deploy' || rel.status === 'deployed') {
                  <button nz-button nzType="primary" nzSize="small" (click)="openConfirmDeployModal(rel.id)">
                    <span nz-icon nzType="cloud-server"></span>确认部署
                  </button>
                }
              </div>
            </div>

            @if (rel.status === 'publish_failed' && rel.failureReason) {
              <nz-alert
                nzType="error"
                nzShowIcon
                nzMessage="发布流水线失败原因"
                [nzDescription]="rel.failureReason"
                style="margin-bottom: 14px;"
              ></nz-alert>
            }

            <nz-descriptions nzBordered [nzColumn]="2" nzSize="small" style="margin-bottom: 16px;">
              <nz-descriptions-item nzTitle="版本号 / Tag">
                <span class="as-mono">{{ rel.version }} ({{ rel.tagName || rel.version }})</span>
              </nz-descriptions-item>
              <nz-descriptions-item nzTitle="Commit SHA">
                <span class="as-mono">{{ rel.commitSha || '-' }}</span>
              </nz-descriptions-item>
              <nz-descriptions-item nzTitle="最低更新 Agent 版本">
                <span class="as-mono">{{ rel.minAgentVersion || 'v0.1.0' }}</span>
              </nz-descriptions-item>
              <nz-descriptions-item nzTitle="发布完成时间">
                <span class="as-mono">{{ formatDateTime(rel.publishedAt || rel.createdAt) }}</span>
              </nz-descriptions-item>
              <nz-descriptions-item nzTitle="Ed25519 数字签名摘要" [nzSpan]="2">
                <span class="as-mono" style="word-break: break-all;">
                  {{ rel['signatureB64'] || rel.manifestSigB64 || '待签名上传' }}
                </span>
              </nz-descriptions-item>
              <nz-descriptions-item nzTitle="变更日志 (Changelog)" [nzSpan]="2">
                <div style="white-space: pre-wrap;">{{ rel.changelog || '暂无变更日志' }}</div>
              </nz-descriptions-item>
            </nz-descriptions>

            <nz-tabs>
              <nz-tab [nzTitle]="'签名制品包清单 (' + (detail()?.artifacts?.length || 0) + ')'">
                <nz-table
                  #artTable
                  [nzData]="detail()?.artifacts || []"
                  nzSize="small"
                  [nzShowPagination]="false"
                  nzNoResult="暂无落地制品包文件"
                >
                  <thead>
                    <tr>
                      <th>制品包文件名</th>
                      <th nzWidth="120px">文件大小</th>
                      <th>SHA-256 完整性哈希</th>
                      <th nzWidth="160px">上传时间</th>
                    </tr>
                  </thead>
                  <tbody>
                    @for (a of artTable.data; track $index) {
                      <tr>
                        <td class="as-mono"><b>{{ a.fileName }}</b></td>
                        <td class="as-mono">{{ formatBytes(a.sizeBytes ?? a.fileSize) }}</td>
                        <td class="as-mono" style="font-size: 12px;">{{ a.sha256 }}</td>
                        <td class="as-mono">{{ formatDateTime(a.createdAt) }}</td>
                      </tr>
                    }
                  </tbody>
                </nz-table>
              </nz-tab>

              <nz-tab [nzTitle]="'包含需求 (' + (detail()?.requirements?.length || 0) + ')'">
                <nz-table
                  #rReqTable
                  [nzData]="detail()?.requirements || []"
                  nzSize="small"
                  [nzShowPagination]="false"
                  nzNoResult="无关联需求记录"
                >
                  <thead>
                    <tr>
                      <th nzWidth="90px">需求 ID</th>
                      <th>标题</th>
                      <th nzWidth="120px">模块</th>
                    </tr>
                  </thead>
                  <tbody>
                    @for (rq of rReqTable.data; track rq.id) {
                      <tr>
                        <td class="as-mono">#{{ rq.id }}</td>
                        <td>{{ rq.title }}</td>
                        <td class="as-mono">{{ rq.moduleCode || '-' }}</td>
                      </tr>
                    }
                  </tbody>
                </nz-table>
              </nz-tab>

              <nz-tab [nzTitle]="'本版部署历史 (' + (detail()?.deployments?.length || 0) + ')'">
                <nz-table
                  #rDepTable
                  [nzData]="detail()?.deployments || []"
                  nzSize="small"
                  [nzShowPagination]="false"
                  nzNoResult="该版本尚未执行过生产部署"
                >
                  <thead>
                    <tr>
                      <th nzWidth="90px">部署 ID</th>
                      <th>部署目标</th>
                      <th nzWidth="140px">部署状态</th>
                      <th nzWidth="110px">数据库备份</th>
                      <th nzWidth="165px">触发时间</th>
                    </tr>
                  </thead>
                  <tbody>
                    @for (dp of rDepTable.data; track dp.id) {
                      <tr>
                        <td class="as-mono">#{{ dp.id }}</td>
                        <td>{{ dp.targetName || ('目标 #' + dp.targetId) }}</td>
                        <td>
                          <nz-tag [nzColor]="getDeploymentStatusMeta(dp.status).color">
                            {{ getDeploymentStatusMeta(dp.status).label }}
                          </nz-tag>
                        </td>
                        <td>{{ dp.backupDb ? '已勾选备份' : '不备份' }}</td>
                        <td class="as-mono">{{ formatDateTime(dp.startedAt || dp.createdAt) }}</td>
                      </tr>
                    }
                  </tbody>
                </nz-table>
              </nz-tab>
            </nz-tabs>
          }
        </nz-spin>
      </ng-container>
    </nz-drawer>
  `,
  styles: [
    `
      .deploy-meta-bar {
        display: flex;
        justify-content: space-between;
        align-items: center;
        flex-wrap: wrap;
        gap: 12px;
        background: #f0f5ff;
        border: 1px solid #adc6ff;
        border-radius: 8px;
        padding: 10px 14px;
        margin-bottom: 14px;
      }
      .backup-check-box {
        margin-top: 14px;
        padding: 12px 14px;
        background: #fffbe6;
        border: 1px solid #ffe58f;
        border-radius: 8px;
      }
    `
  ]
})
export class ReleasesComponent implements OnInit {
  private readonly api = inject(AdminApiService);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);
  private readonly message = inject(NzMessageService);

  readonly products = signal<Product[]>([]);
  readonly items = signal<ReleaseItem[]>([]);
  readonly total = signal<number>(0);
  readonly page = signal<number>(1);
  readonly pageSize = signal<number>(20);
  readonly loading = signal<boolean>(false);
  readonly submitting = signal<boolean>(false);

  filterProductId: number | null = null;
  filterStatus: string | null = null;

  // 详情抽屉
  readonly drawerVisible = signal<boolean>(false);
  readonly detailLoading = signal<boolean>(false);
  readonly detail = signal<ReleaseDetailBundle | null>(null);

  // 新建修复任务弹窗
  readonly fixModalVisible = signal<boolean>(false);
  readonly activeFixRelease = signal<ReleaseItem | null>(null);
  fixWorkNote = '';

  // 确认部署弹窗
  readonly deployModalVisible = signal<boolean>(false);
  readonly deployModalLoading = signal<boolean>(false);
  readonly deployBundle = signal<ReleaseDetailBundle | null>(null);
  readonly fallbackTargets = signal<DeployTarget[]>([]);
  readonly selectedTargetIds = signal<number[]>([]);
  confirmBackupDb = false;

  /** 聚合当前确认部署弹窗可选的部署目标列表 */
  readonly availableTargets = computed<DeployTarget[]>(() => {
    const fromBundle = this.deployBundle()?.deployTargets;
    if (Array.isArray(fromBundle) && fromBundle.length > 0) {
      return fromBundle;
    }
    return this.fallbackTargets();
  });

  /** 判断当前选中的部署目标中是否存在备份存储占用超过配额上限的节点 */
  readonly hasStorageExceededWarning = computed<boolean>(() => {
    const selIds = this.selectedTargetIds();
    const targets = this.availableTargets().filter((t) => selIds.includes(t.id));
    return targets.some((t) => this.isTargetStorageExceeded(t));
  });

  readonly formatBytes = formatBytes;
  readonly formatDateTime = formatDateTime;
  readonly getReleaseStatusMeta = getReleaseStatusMeta;
  readonly getMigrationRiskMeta = getMigrationRiskMeta;
  readonly getDeploymentStatusMeta = getDeploymentStatusMeta;

  /**
   * 组件初始化时加载产品档案及路由筛选参数。
   * @returns void
   */
  ngOnInit(): void {
    this.api.listProducts().subscribe({
      next: (list) => this.products.set(list),
      error: () => {}
    });

    this.route.queryParamMap.subscribe((q) => {
      const st = q.get('status');
      if (st) {
        this.filterStatus = st;
      }
      this.loadList();
    });
  }

  /**
   * 查询版本分页数据。
   * @returns void
   */
  loadList(): void {
    this.loading.set(true);
    this.api
      .listReleases({
        productId: this.filterProductId ?? undefined,
        status: this.filterStatus ?? undefined,
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
          this.message.error(err.message || '加载版本列表失败');
        }
      });
  }

  /**
   * 从第 1 页重新检索。
   * @returns void
   */
  searchFromFirstPage(): void {
    this.page.set(1);
    this.loadList();
  }

  /**
   * 重置筛选器。
   * @returns void
   */
  resetFilters(): void {
    this.filterProductId = null;
    this.filterStatus = null;
    this.page.set(1);
    this.loadList();
  }

  /**
   * 页码切换处理。
   * @param idx 新页码
   * @returns void
   */
  onPageChange(idx: number): void {
    this.page.set(idx);
    this.loadList();
  }

  /**
   * 每页条数切换处理。
   * @param size 新每页条数
   * @returns void
   */
  onPageSizeChange(size: number): void {
    this.pageSize.set(size);
    this.page.set(1);
    this.loadList();
  }

  /**
   * 获取产品显示名称。
   * @param productId 产品主键 ID
   * @param fallback 自带产品名称
   * @returns 产品中文名
   */
  getProductName(productId: number, fallback?: string): string {
    if (fallback) {
      return fallback;
    }
    return this.products().find((p) => p.id === productId)?.name || `产品 #${productId}`;
  }

  /**
   * 打开版本详情抽屉。
   * @param id 版本主键 ID
   * @returns void
   */
  openDetail(id: number): void {
    this.drawerVisible.set(true);
    this.detailLoading.set(true);
    this.api.getReleaseDetail(id).subscribe({
      next: (bundle) => {
        this.detailLoading.set(false);
        this.detail.set(bundle);
      },
      error: (err: Error) => {
        this.detailLoading.set(false);
        this.message.error(err.message || '加载版本详情失败');
      }
    });
  }

  /**
   * 打开针对 `publish_failed` 版本新建关联修复任务的弹窗。
   * @param rel 发布失败的版本对象
   * @returns void
   */
  openCreateFixModal(rel: ReleaseItem): void {
    this.activeFixRelease.set(rel);
    this.fixWorkNote = `修复版本 ${rel.version} 发布流水线故障并发布新修订号`;
    this.fixModalVisible.set(true);
  }

  /**
   * 提交创建关联修复开发任务请求。
   * @returns void
   */
  submitCreateFixWork(): void {
    const rel = this.activeFixRelease();
    if (!rel) {
      return;
    }
    this.submitting.set(true);
    this.api.createFixWorkForRelease(rel.id, { note: this.fixWorkNote.trim() }).subscribe({
      next: (res) => {
        this.submitting.set(false);
        this.fixModalVisible.set(false);
        const newWorkId = Number(res?.['workItemId'] || res?.['id'] || 0);
        this.message.success(
          newWorkId > 0 ? `已新建关联修复开发任务 #${newWorkId}` : '已新建关联修复开发任务'
        );
        this.loadList();
        if (this.drawerVisible() && this.detail()?.release?.id === rel.id) {
          this.openDetail(rel.id);
        }
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '新建关联修复任务失败');
      }
    });
  }

  /**
   * 打开“确认部署”弹窗，加载该版本的变更日志、包含需求及该产品的全部生产部署目标。
   * @param releaseId 版本主键 ID
   * @returns void
   */
  openConfirmDeployModal(releaseId: number): void {
    this.deployModalVisible.set(true);
    this.deployModalLoading.set(true);
    this.fallbackTargets.set([]);
    this.api.getReleaseDetail(releaseId).subscribe({
      next: (bundle) => {
        this.deployModalLoading.set(false);
        this.deployBundle.set(bundle);
        // 若迁移风险为含数据改写（data_rewrite），默认自动勾选先备份数据库
        const risk = (bundle.release?.migrationRisk || '').toLowerCase();
        this.confirmBackupDb = risk === 'data_rewrite' || risk === 'rewrite';

        if (Array.isArray(bundle.deployTargets) && bundle.deployTargets.length > 0) {
          this.selectedTargetIds.set(bundle.deployTargets.map((t) => t.id));
        } else if (bundle.release?.productId) {
          this.api.listDeployTargets(bundle.release.productId).subscribe({
            next: (targets) => {
              this.fallbackTargets.set(targets);
              this.selectedTargetIds.set(targets.map((t) => t.id));
            },
            error: () => {}
          });
        }
      },
      error: (err: Error) => {
        this.deployModalLoading.set(false);
        this.message.error(err.message || '加载部署预检信息失败');
      }
    });
  }

  /**
   * 勾选或取消勾选某个生产部署目标。
   * @param targetId 部署目标 ID
   * @param checked 是否选中
   * @returns void
   */
  toggleTargetSelection(targetId: number, checked: boolean): void {
    const current = new Set(this.selectedTargetIds());
    if (checked) {
      current.add(targetId);
    } else {
      current.delete(targetId);
    }
    this.selectedTargetIds.set(Array.from(current));
  }

  /**
   * 判断某个部署目标的数据库备份目录占用是否超出配置的配额上限。
   * @param t 部署目标对象
   * @returns 超限返回 true
   */
  isTargetStorageExceeded(t: DeployTarget): boolean {
    if (t.storageExceeded || t['quotaExceeded'] || t['backupQuotaExceeded']) {
      return true;
    }
    if (t.backupQuotaMb && t.backupUsedBytes && t.backupUsedBytes > t.backupQuotaMb * 1024 * 1024) {
      return true;
    }
    if (t.backupQuotaBytes && t.backupUsedBytes && t.backupUsedBytes > t.backupQuotaBytes) {
      return true;
    }
    if (t.backupQuotaMb && t.backupUsedMb && t.backupUsedMb > t.backupQuotaMb) {
      return true;
    }
    return false;
  }

  /**
   * 格式化部署目标的数据库备份已用与配额上限显示文本。
   * @param t 部署目标对象
   * @returns 易读字符串
   */
  formatTargetBackup(t: DeployTarget): string {
    if (t.backupUsedBytes !== undefined && t.backupQuotaMb) {
      return `${formatBytes(t.backupUsedBytes || 0)} / ${t.backupQuotaMb} MB`;
    }
    if (t.backupQuotaBytes || t.backupUsedBytes) {
      return `${formatBytes(t.backupUsedBytes || 0)} / ${formatBytes(t.backupQuotaBytes || 0)}`;
    }
    if (t.backupQuotaMb || t.backupUsedMb) {
      return `${t.backupUsedMb || 0} MB / ${t.backupQuotaMb || 0} MB`;
    }
    return '正常';
  }

  /**
   * 提交确认部署请求，向选定目标的更新 Agent 下发升级指令。
   * @returns void
   */
  submitConfirmDeploy(): void {
    const bundle = this.deployBundle();
    if (!bundle?.release) {
      return;
    }
    const targetIds = this.selectedTargetIds();
    if (targetIds.length === 0) {
      this.message.warning('请至少勾选一个要升级的生产部署目标');
      return;
    }
    this.submitting.set(true);
    this.api
      .confirmReleaseDeploy(bundle.release.id, {
        targetIds,
        backupDb: this.confirmBackupDb
      })
      .subscribe({
        next: () => {
          this.submitting.set(false);
          this.deployModalVisible.set(false);
          this.message.success('确认部署成功！升级指令已投递至更新 Agent 队列');
          this.loadList();
          void this.router.navigate(['/deployments']);
        },
        error: (err: Error) => {
          this.submitting.set(false);
          this.message.error(err.message || '下发确认部署指令失败');
        }
      });
  }

  /**
   * 跳转查看关联的修复开发任务。
   * @param workItemId 开发任务主键 ID
   * @returns void
   */
  jumpToWorkItem(workItemId: number): void {
    void this.router.navigate(['/work-items'], { queryParams: { workItemId } });
  }
}

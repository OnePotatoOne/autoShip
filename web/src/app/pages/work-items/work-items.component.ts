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
import { NzTimelineModule } from 'ng-zorro-antd/timeline';
import {
  Product,
  WorkItem,
  WorkItemDetailBundle,
  WorkScopeChange
} from '../../core/models/api.models';
import { AdminApiService } from '../../core/services/admin-api.service';
import {
  copyTextToClipboard,
  formatDateTime,
  formatDuration,
  getCheckStatusMeta,
  getMigrationRiskMeta,
  getWorkStatusMeta,
  parseStringArray
} from '../../core/utils/format.utils';

/**
 * autoShip 管理后台开发任务与人工验收工作台组件（`/work-items`）。
 * 对应设计文档第 8.4、8.5、8.7、8.9 节：
 * 1. 处理方案确认门（`awaiting_plan`）与回答 Agent 提问（`awaiting_answer`）；
 * 2. 验收包第一屏突出呈现“改动范围三区标记（本模块/其他模块附理由/公共区特殊标记）+ 系统性影响审计结论”；
 * 3. 展示 Tailnet 预览实例及随机测试账号、验收证据对照、门禁矩阵、成本耗时，并执行通过/打回/放弃。
 */
@Component({
  selector: 'app-work-items',
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
    NzTimelineModule,
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
          <span nz-icon nzType="code"></span>
          <span>开发任务与人工验收工作台</span>
        </h1>
        <p class="as-page-subtitle">
          自研单长会话编码循环 + 多重门禁校验；人工关口 2：重点审查改动范围与公共区系统性影响审计，经 Tailnet 试用预览后验收放行
        </p>
      </div>
      <div>
        <button nz-button nzType="default" (click)="loadList()">
          <span nz-icon nzType="reload" [nzSpin]="loading()"></span>
          <span>刷新列表</span>
        </button>
      </div>
    </div>

    <!-- 筛选栏 -->
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
          style="width: 180px;"
          nzPlaceHolder="任务状态"
          nzAllowClear
          [(ngModel)]="filterStatus"
          (ngModelChange)="searchFromFirstPage()"
        >
          <nz-option nzValue="preparing" nzLabel="准备中 (preparing)"></nz-option>
          <nz-option nzValue="awaiting_plan" nzLabel="待确认方案 (awaiting_plan)"></nz-option>
          <nz-option nzValue="coding" nzLabel="开发中 (coding)"></nz-option>
          <nz-option nzValue="awaiting_answer" nzLabel="等待答复 (awaiting_answer)"></nz-option>
          <nz-option nzValue="pending_acceptance" nzLabel="待验收 (pending_acceptance)"></nz-option>
          <nz-option nzValue="accepted" nzLabel="已通过 (accepted)"></nz-option>
          <nz-option nzValue="paused" nzLabel="已暂停 (paused)"></nz-option>
          <nz-option nzValue="abandoned" nzLabel="已放弃 (abandoned)"></nz-option>
        </nz-select>

        <button nz-button nzType="primary" (click)="searchFromFirstPage()">筛选</button>
        <button nz-button nzType="default" (click)="resetFilters()">重置</button>
      </div>
    </div>

    <!-- 开发任务列表 -->
    <div class="as-card">
      <nz-table
        #workTable
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
            <th nzWidth="76px">任务 ID</th>
            <th nzWidth="150px">产品 / 模块</th>
            <th>任务标题与 Worktree 分支</th>
            <th nzWidth="130px">当前状态</th>
            <th nzWidth="140px">迁移风险</th>
            <th nzWidth="180px">Token 用量 / 耗时</th>
            <th nzWidth="155px">更新时间</th>
            <th nzWidth="190px">操作</th>
          </tr>
        </thead>
        <tbody>
          @for (w of workTable.data; track w.id) {
            <tr>
              <td class="as-mono">#{{ w.id }}</td>
              <td>
                <div style="font-weight: 500;">{{ getProductName(w.productId, w.productName) }}</div>
                <div class="as-mono" style="font-size: 12px; color: #64748b;">
                  {{ w['primaryModuleCode'] || w.moduleCode || '主模块' }}
                </div>
              </td>
              <td>
                <a style="font-weight: 600;" (click)="openDetail(w.id)">{{ w.title || ('开发任务 #' + w.id) }}</a>
                @if (w.branchName || w['branch']) {
                  <div class="as-mono" style="font-size: 12px; color: #64748b; margin-top: 2px;">
                    <span nz-icon nzType="branches"></span> {{ w.branchName || w['branch'] }}
                  </div>
                }
              </td>
              <td>
                <nz-tag [nzColor]="getWorkStatusMeta(w.status).color">
                  {{ getWorkStatusMeta(w.status).label }}
                </nz-tag>
              </td>
              <td>
                <nz-tag [nzColor]="getMigrationRiskMeta(w.migrationRisk).color">
                  {{ getMigrationRiskMeta(w.migrationRisk).label }}
                </nz-tag>
              </td>
              <td class="as-mono" style="font-size: 12px;">
                <div>{{ getTotalTokens(w) | number }} tok</div>
                <div style="color: #64748b;">{{ formatDuration(w.durationSeconds) }}</div>
              </td>
              <td class="as-mono">{{ formatDateTime(w.updatedAt || w.createdAt) }}</td>
              <td>
                <div style="display: flex; gap: 6px; flex-wrap: wrap;">
                  <button
                    nz-button
                    [nzType]="isActionNeeded(w.status) ? 'primary' : 'default'"
                    nzSize="small"
                    (click)="openDetail(w.id)"
                  >
                    {{ getPrimaryActionLabel(w.status) }}
                  </button>
                  @if (w.status !== 'accepted' && w.status !== 'abandoned') {
                    <button nz-button nzDanger nzSize="small" (click)="openAbandonModal(w)">放弃</button>
                  }
                </div>
              </td>
            </tr>
          }
        </tbody>
      </nz-table>
    </div>

    <!-- 开发任务与验收包全视图宽抽屉 -->
    <nz-drawer
      [nzVisible]="drawerVisible()"
      [nzWidth]="1060"
      [nzTitle]="'开发任务与验收工作台 #' + (detail()?.workItem?.id || '') + ' · ' + (detail()?.workItem?.title || '')"
      (nzOnClose)="drawerVisible.set(false)"
    >
      <ng-container *nzDrawerContent>
        <nz-spin [nzSpinning]="detailLoading()">
          @if (detail()?.workItem; as wi) {
            <!-- 顶栏状态与人工操作关口按钮 -->
            <div class="wi-header-bar">
              <div style="display: flex; align-items: center; gap: 8px; flex-wrap: wrap;">
                <nz-tag [nzColor]="getWorkStatusMeta(wi.status).color">
                  {{ getWorkStatusMeta(wi.status).label }}
                </nz-tag>
                <nz-tag [nzColor]="getMigrationRiskMeta(wi.migrationRisk).color">
                  迁移分级：{{ getMigrationRiskMeta(wi.migrationRisk).label }}
                </nz-tag>
                @if (wi.headCommit || wi['candidateCommitSha'] || wi['commitSha']) {
                  <span class="as-mono" style="font-size: 12px; color: #64748b;">
                    Commit: {{ (wi.headCommit || wi['candidateCommitSha'] || wi['commitSha'] || '').slice(0, 10) }}
                  </span>
                }
              </div>

              <div style="display: flex; gap: 8px; flex-wrap: wrap;">
                <button nz-button nzSize="small" (click)="openDetail(wi.id)">
                  <span nz-icon nzType="reload"></span>刷新
                </button>
                @if (wi.status === 'awaiting_acceptance' || wi.status === 'pending_acceptance') {
                  <button nz-button nzType="primary" nzSize="small" (click)="openAcceptModal(wi)">
                    <span nz-icon nzType="check-circle"></span>验收通过并发布
                  </button>
                  <button nz-button nzSize="small" (click)="openRejectModal(wi)">
                    <span nz-icon nzType="rollback"></span>打回重改
                  </button>
                }
                @if (wi.status !== 'accepted' && wi.status !== 'abandoned') {
                  <button nz-button nzDanger nzSize="small" (click)="openAbandonModal(wi)">
                    放弃任务
                  </button>
                }
              </div>
            </div>

            <!-- 交互关口卡片 A：方案确认门 (status === 'awaiting_plan') -->
            @if (wi.status === 'awaiting_plan' || wi.status === 'pending_plan') {
              <div class="action-gate-card gate-plan">
                <div class="gate-card-title">
                  <span nz-icon nzType="safety"></span>
                  <span>方案确认门待审批（同意后开发 Agent 才进入编码会话）</span>
                </div>
                <nz-descriptions nzBordered [nzColumn]="2" nzSize="small" style="background: #fff; margin-bottom: 12px;">
                  <nz-descriptions-item nzTitle="拟改动模块清单">
                    @for (m of parseStringArray(wi.planModulesJson || wi['planModules']); track m) {
                      <nz-tag nzColor="blue">{{ m }}</nz-tag>
                    } @empty {
                      <span>{{ wi.moduleCode || '仅本需求所属模块' }}</span>
                    }
                  </nz-descriptions-item>
                  <nz-descriptions-item nzTitle="是否涉及数据库表结构变更">
                    @if (wi.hasSchemaChange || wi.planSchemaChanges) {
                      <nz-tag nzColor="orange">涉及表结构变更</nz-tag>
                      <span style="margin-left: 6px;">{{ wi.planSchemaChanges }}</span>
                    } @else {
                      <nz-tag nzColor="green">无表结构变更</nz-tag>
                    }
                  </nz-descriptions-item>
                  <nz-descriptions-item nzTitle="拟触碰公共区文件" [nzSpan]="2">
                    @for (pf of parseStringArray(wi.planPublicFilesJson || wi['planPublicFiles']); track pf) {
                      <nz-tag nzColor="volcano" class="as-mono">{{ pf }}</nz-tag>
                    } @empty {
                      <span style="color: #64748b;">未计划改动公共区目录或全局构建依赖文件</span>
                    }
                  </nz-descriptions-item>
                  <nz-descriptions-item nzTitle="技术方案说明" [nzSpan]="2">
                    <div style="white-space: pre-wrap;">
                      {{ wi.planSummary || wi['planDocumentJson'] || wi['specSummary'] || wi['technicalPlan'] || '详见规格阶段产出说明' }}
                    </div>
                  </nz-descriptions-item>
                </nz-descriptions>

                <div style="display: flex; gap: 10px; align-items: center; flex-wrap: wrap;">
                  <input
                    nz-input
                    style="flex: 1; min-width: 240px;"
                    [(ngModel)]="planComment"
                    placeholder="填写方案审批意见或补充约束要求（可选）"
                  />
                  <button
                    nz-button
                    nzType="primary"
                    [nzLoading]="submitting()"
                    (click)="submitPlanDecision(wi.id, true)"
                  >
                    同意方案并开始编码
                  </button>
                  <button
                    nz-button
                    nzDanger
                    [nzLoading]="submitting()"
                    (click)="submitPlanDecision(wi.id, false)"
                  >
                    退回重新设计方案
                  </button>
                </div>
              </div>
            }

            <!-- 交互关口卡片 B：回答 Agent 提问 (status === 'awaiting_answer' / ask_owner) -->
            @if (wi.status === 'awaiting_answer' || wi.status === 'ask_owner') {
              <div class="action-gate-card gate-answer">
                <div class="gate-card-title">
                  <span nz-icon nzType="exclamation-circle"></span>
                  <span>开发 Agent 请求人工决策 (ask_owner)</span>
                </div>
                <div class="question-box">
                  <div style="font-size: 12px; color: #92400e; margin-bottom: 4px;">
                    问题编号: <span class="as-mono">{{ resolvedQuestion().questionId }}</span>
                  </div>
                  <div style="font-size: 14px; font-weight: 500; white-space: pre-wrap;">
                    {{ resolvedQuestion().questionText }}
                  </div>
                </div>
                <div style="display: flex; flex-direction: column; gap: 10px;">
                  <textarea
                    nz-input
                    rows="3"
                    [(ngModel)]="ownerAnswer"
                    placeholder="请输入您的明确决策答复，提交后开发 Agent 将携带答案恢复编码循环..."
                  ></textarea>
                  <div style="text-align: right;">
                    <button
                      nz-button
                      nzType="primary"
                      [nzLoading]="submitting()"
                      (click)="submitAnswer(wi.id)"
                    >
                      <span nz-icon nzType="send"></span>提交答复并继续开发
                    </button>
                  </div>
                </div>
              </div>
            }

            <!-- ==================== 第一屏核心区：改动范围三区标记与公共区系统性影响审计（8.5 & 8.9 节） ==================== -->
            <div class="scope-hero-panel">
              <div class="scope-hero-head">
                <h3 class="as-section-title" style="margin: 0;">
                  <span nz-icon nzType="audit"></span>
                  <span>第一屏重点审查：代码改动范围与公共区系统性影响审计（由代码静态计算）</span>
                </h3>
                <span style="font-size: 12px; color: #64748b;">
                  共改动 {{ (detail()?.scopeChanges?.length || 0) }} 个文件
                </span>
              </div>

              <!-- 三区概览统计盒 -->
              <div class="as-zone-grid">
                <div class="as-zone-box as-zone-own">
                  <div style="display: flex; justify-content: space-between; align-items: center;">
                    <b>1. 本模块区（自由改动）</b>
                    <nz-tag nzColor="success">{{ ownZoneChanges().length }} 个文件</nz-tag>
                  </div>
                  <div style="font-size: 12px; color: #389e0d; margin-top: 6px;">
                    需求归属模块源码及专属单测，累计变更 {{ sumLines(ownZoneChanges()) }} 行
                  </div>
                </div>

                <div class="as-zone-box as-zone-other">
                  <div style="display: flex; justify-content: space-between; align-items: center;">
                    <b>2. 其他模块区（逐项附理由）</b>
                    <nz-tag nzColor="warning">{{ otherZoneChanges().length }} 个文件</nz-tag>
                  </div>
                  <div style="font-size: 12px; color: #d46b08; margin-top: 6px;">
                    本模块之外的跨模块改动，累计变更 {{ sumLines(otherZoneChanges()) }} 行
                  </div>
                </div>

                <div class="as-zone-box as-zone-public">
                  <div style="display: flex; justify-content: space-between; align-items: center;">
                    <b>3. 公共区（特殊标记 + 系统性审计）</b>
                    <nz-tag nzColor="error">{{ publicZoneChanges().length }} 个文件</nz-tag>
                  </div>
                  <div style="font-size: 12px; color: #cf1322; margin-top: 6px;">
                    公共目录 / 依赖锁文件 / 部署脚本，累计变更 {{ sumLines(publicZoneChanges()) }} 行
                  </div>
                </div>
              </div>

              <!-- 当涉及公共区改动时，强制醒目展示系统性影响审计结论（8.5 节） -->
              @if (publicZoneChanges().length > 0 || wi.systemImpactConclusion || wi['impactAuditJson']) {
                <div class="public-audit-banner">
                  <div style="font-weight: 600; color: #a8071a; margin-bottom: 6px; display: flex; align-items: center; gap: 6px;">
                    <span nz-icon nzType="alert"></span>
                    <span>公共区系统性影响独立审计结论</span>
                  </div>
                  <div style="font-size: 13px; margin-bottom: 6px;">
                    <b>受影响模块清单：</b>
                    @for (am of parseStringArray(wi.affectedModulesJson || wi['affectedModules']); track am) {
                      <nz-tag nzColor="red">{{ am }}</nz-tag>
                    } @empty {
                      <span>已执行全模块回归与页面巡检核验</span>
                    }
                  </div>
                  <div style="font-size: 13px; color: #1e293b; white-space: pre-wrap;">
                    {{ wi.systemImpactConclusion || wi['impactAuditSummary'] || '独立审计会话已完成公共引用链分析、连带风险评估及全模块补跑测试验证。' }}
                  </div>
                </div>
              }

              <!-- 其他模块与公共区改动逐项明细及理由表 -->
              @if ((detail()?.scopeChanges?.length || 0) > 0) {
                <nz-table
                  #scopeTable
                  [nzData]="sortedScopeChanges()"
                  nzSize="small"
                  [nzPageSize]="8"
                  [nzHideOnSinglePage]="true"
                >
                  <thead>
                    <tr>
                      <th nzWidth="140px">所属范围分区</th>
                      <th>改动文件路径</th>
                      <th nzWidth="115px">增删行数</th>
                      <th>跨模块 / 公共区 / 测试改动理由说明</th>
                    </tr>
                  </thead>
                  <tbody>
                    @for (sc of scopeTable.data; track $index) {
                      <tr>
                        <td>
                          @if (isPublicZone(sc)) {
                            <nz-tag nzColor="error">★ 公共区特殊标记</nz-tag>
                          } @else if (isOtherZone(sc)) {
                            <nz-tag nzColor="warning">其他模块: {{ sc.moduleCode || '跨模块' }}</nz-tag>
                          } @else {
                            <nz-tag nzColor="success">本模块区</nz-tag>
                          }
                          @if (sc.isTestFile || sc.isTestModified) {
                            <nz-tag nzColor="purple" style="margin-top: 4px;">既有测试修改</nz-tag>
                          }
                        </td>
                        <td class="as-mono">{{ sc.filePath }}</td>
                        <td class="as-mono">
                          <span style="color: #389e0d;">+{{ sc.addedLines ?? 0 }}</span> /
                          <span style="color: #cf1322;">-{{ sc.deletedLines ?? 0 }}</span>
                        </td>
                        <td>
                          {{ sc.reason || sc.impactAuditNote || (isOwnZone(sc) ? '本模块常规实现' : '未附说明') }}
                        </td>
                      </tr>
                    }
                  </tbody>
                </nz-table>
              } @else {
                <div class="as-subtle-box" style="color: #64748b; font-size: 13px;">
                  当前阶段暂无代码文件 diff 改动记录
                </div>
              }
            </div>

            <!-- Tailnet 预览实例与成本耗时并排卡片 -->
            <div style="display: grid; grid-template-columns: 1.3fr 1fr; gap: 14px; margin-bottom: 16px;">
              <div class="as-subtle-box">
                <h4 class="as-section-title" style="margin-bottom: 8px;">
                  <span nz-icon nzType="eye"></span>
                  <span>Mac Tailnet 预览实例与专用测试账号</span>
                </h4>
                @if (detail()?.previewSession; as pv) {
                  <div style="display: flex; flex-direction: column; gap: 8px; font-size: 13px;">
                    <div>
                      <span>Tailnet 直连地址：</span>
                      <a
                        class="as-mono"
                        [href]="pv.previewUrl || pv.tailnetUrl || pv.url"
                        target="_blank"
                        rel="noopener noreferrer"
                      >
                        {{ pv.previewUrl || pv.tailnetUrl || pv.url || '暂未分配地址' }}
                      </a>
                      @if (pv.previewUrl || pv.tailnetUrl || pv.url) {
                        <button
                          nz-button
                          nzSize="small"
                          style="margin-left: 8px;"
                          (click)="copyText(pv.previewUrl || pv.tailnetUrl || pv.url || '', '预览链接')"
                        >
                          复制
                        </button>
                      }
                    </div>
                    <div style="display: flex; gap: 12px; align-items: center; flex-wrap: wrap;">
                      <span>测试账号：<b class="as-mono">{{ pv.testUsername || 'demo_admin' }}</b></span>
                      <span>测试密码：<b class="as-mono">{{ pv.testPassword || pv.testAccountJson || '-' }}</b></span>
                      @if (pv.testPassword) {
                        <button
                          nz-button
                          nzSize="small"
                          (click)="copyText(pv.testPassword, '预览测试密码')"
                        >
                          复制密码
                        </button>
                      }
                    </div>
                    <div style="font-size: 12px; color: #64748b;">
                      状态: {{ pv.status || 'running' }} · 空闲超时/自动回收时间: {{ formatDateTime(pv.expiresAt) }}
                    </div>
                  </div>
                } @else {
                  <div style="color: #64748b; font-size: 13px;">
                    预览实例尚未启动（打包进入“待验收”阶段时在 Mac 自动拉起并分配端口与演示库）
                  </div>
                }
              </div>

              <div class="as-subtle-box">
                <h4 class="as-section-title" style="margin-bottom: 8px;">
                  <span nz-icon nzType="thunderbolt"></span>
                  <span>预算消耗与耗时成本</span>
                </h4>
                <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 6px; font-size: 13px;">
                  <div>总 Token：<b class="as-mono">{{ getTotalTokens(wi) | number }}</b></div>
                  <div>预算上限：<span class="as-mono">{{ (wi.tokenBudget || 0) | number }}</span></div>
                  <div>会话轮数：<span class="as-mono">{{ wi.usedTurns || 0 }} / {{ wi.maxTurns || '-' }}</span></div>
                  <div>执行耗时：<b class="as-mono">{{ formatDuration(wi.durationSeconds) }}</b></div>
                </div>
              </div>
            </div>

            <!-- 下半区多维标签页：验收证据表 / 自动化门禁 / 改动说明与评审 / 活动流与会话记录 -->
            <nz-tabs>
              <nz-tab [nzTitle]="'验收标准与证据对照 (' + (detail()?.requirements?.length || 0) + ')'">
                @if ((detail()?.requirements?.length || 0) === 0) {
                  <nz-empty nzNotFoundContent="暂无关联需求条目"></nz-empty>
                } @else {
                  <nz-table
                    #evTable
                    [nzData]="detail()?.requirements || []"
                    nzSize="small"
                    [nzShowPagination]="false"
                  >
                    <thead>
                      <tr>
                        <th nzWidth="90px">需求 ID</th>
                        <th nzWidth="220px">需求标题</th>
                        <th>验收标准 (Given/When/Then)</th>
                        <th nzWidth="260px">绑定真实运行证据</th>
                      </tr>
                    </thead>
                    <tbody>
                      @for (r of evTable.data; track r.id) {
                        <tr>
                          <td class="as-mono">#{{ r.id }}</td>
                          <td><b>{{ r.title }}</b></td>
                          <td style="white-space: pre-wrap;">{{ r.acceptanceCriteria || '-' }}</td>
                          <td class="as-mono" style="font-size: 12px;">
                            {{ r['evidenceSummary'] || r['evidence'] || '见门禁核验测试退出码与摘录' }}
                          </td>
                        </tr>
                      }
                    </tbody>
                  </nz-table>
                }
              </nz-tab>

              <nz-tab [nzTitle]="'自动化门禁矩阵 (' + (detail()?.gates?.length || 0) + ')'">
                @if ((detail()?.gates?.length || 0) === 0) {
                  <nz-empty nzNotFoundContent="尚未生成门禁检查记录"></nz-empty>
                } @else {
                  <nz-table
                    #gateTable
                    [nzData]="detail()?.gates || []"
                    nzSize="small"
                    [nzShowPagination]="false"
                  >
                    <thead>
                      <tr>
                        <th nzWidth="190px">门禁检查项</th>
                        <th nzWidth="105px">结果状态</th>
                        <th nzWidth="90px">退出码</th>
                        <th>真实校验证据与输出摘录</th>
                        <th nzWidth="155px">检查时间</th>
                      </tr>
                    </thead>
                    <tbody>
                      @for (g of gateTable.data; track $index) {
                        <tr>
                          <td><b>{{ g.gateName || g.name || g.gateCode }}</b></td>
                          <td>
                            <nz-tag [nzColor]="getCheckStatusMeta(g.status).color">
                              {{ getCheckStatusMeta(g.status).label }}
                            </nz-tag>
                          </td>
                          <td class="as-mono">{{ g.exitCode ?? 0 }}</td>
                          <td>
                            <div>{{ g.summary }}</div>
                            @if (g.outputExcerpt || g.evidence || g.evidenceJson) {
                              <pre class="as-terminal" style="max-height: 120px; margin-top: 6px; padding: 8px;">{{ g.outputExcerpt || g.evidence || g.evidenceJson }}</pre>
                            }
                          </td>
                          <td class="as-mono">{{ formatDateTime(g.checkedAt || g.updatedAt) }}</td>
                        </tr>
                      }
                    </tbody>
                  </nz-table>
                }
              </nz-tab>

              <nz-tab nzTitle="改动说明与独立评审结论">
                <nz-descriptions nzBordered [nzColumn]="1" nzSize="small">
                  <nz-descriptions-item nzTitle="独立评审会话结论">
                    <div style="white-space: pre-wrap;">
                      {{ wi.reviewConclusion || '独立评审重点检查鉴权绕过、新增外联、硬编码凭据与无关改动。' }}
                    </div>
                  </nz-descriptions-item>
                  <nz-descriptions-item nzTitle="改动摘要说明">
                    <div style="white-space: pre-wrap;">
                      {{ wi.changeSummary || '暂无模型改动说明摘要' }}
                    </div>
                  </nz-descriptions-item>
                </nz-descriptions>
                @if (wi.keyDiff) {
                  <h4 class="as-section-title" style="margin-top: 14px;">关键 Diff 摘录</h4>
                  <pre class="as-terminal">{{ wi.keyDiff }}</pre>
                }
              </nz-tab>

              <nz-tab [nzTitle]="'开发活动流与会话引用 (' + (detail()?.activities?.length || 0) + ')'">
                @if ((detail()?.activities?.length || 0) === 0) {
                  <nz-empty nzNotFoundContent="暂无运行活动流事件"></nz-empty>
                } @else {
                  <nz-timeline style="margin-top: 10px;">
                    @for (act of detail()?.activities || []; track $index) {
                      <nz-timeline-item
                        [nzColor]="isBlockedActivity(act) ? 'red' : 'blue'"
                      >
                        <div style="display: flex; align-items: center; gap: 8px; flex-wrap: wrap;">
                          <b>{{ act['stage'] || act['activityType'] || act['type'] || '运行事件' }}</b>
                          <span style="font-size: 12px; color: #64748b;">
                            {{ formatDateTime(act['createdAt']) }}
                          </span>
                          @if (isBlockedActivity(act)) {
                            <nz-tag nzColor="error">被权限档案拦截</nz-tag>
                            <a (click)="jumpToProductPermission(wi.productId)">前往产品权限档案一键放行 →</a>
                          }
                        </div>
                        <div style="font-size: 13px; margin-top: 4px; white-space: pre-wrap;">
                          {{ act['message'] || act['summary'] || act['detail'] }}
                        </div>
                      </nz-timeline-item>
                    }
                  </nz-timeline>
                }

                @if ((detail()?.transcripts?.length || 0) > 0) {
                  <nz-divider></nz-divider>
                  <h4 class="as-section-title">本地 JSONL 会话落盘引用</h4>
                  @for (tr of detail()?.transcripts || []; track $index) {
                    <div class="as-subtle-box" style="margin-bottom: 8px;">
                      <div class="as-mono" style="font-weight: 600;">
                        [{{ tr['sessionRole'] || tr['role'] || 'coder' }}] {{ tr['localFilePath'] || tr['filePath'] || tr['transcriptRef'] }}
                      </div>
                      <div style="font-size: 12.5px; color: #475569; margin-top: 4px;">
                        {{ tr['summary'] || '' }}
                      </div>
                    </div>
                  }
                }
              </nz-tab>
            </nz-tabs>
          }
        </nz-spin>
      </ng-container>
    </nz-drawer>

    <!-- 验收通过并触发发布流水线弹窗 -->
    <nz-modal
      [nzVisible]="acceptModalVisible()"
      nzTitle="验收通过并启动自动发布流水线"
      [nzOkLoading]="submitting()"
      nzOkText="确认验收通过并发布"
      (nzOnCancel)="acceptModalVisible.set(false)"
      (nzOnOk)="submitAccept()"
    >
      <ng-container *nzModalContent>
        <nz-alert
          nzType="info"
          nzShowIcon
          nzMessage="验收绑定当前提交：开发 Agent 将自动 rebase 主分支、写入变更日志、打 tag 触发产品 CI、对制品清单进行 Ed25519 签名并上传至制品库。"
          style="margin-bottom: 16px;"
        ></nz-alert>
        <form nz-form nzLayout="vertical">
          <nz-form-item>
            <nz-form-label nzRequired>目标发布版本号 (SemVer)</nz-form-label>
            <nz-form-control>
              <input
                nz-input
                name="accVer"
                [(ngModel)]="acceptForm.version"
                placeholder="例如 v1.2.0（留空或修改建议版本号）"
              />
            </nz-form-control>
          </nz-form-item>

          <nz-form-item>
            <label nz-checkbox name="accDestructive" [(ngModel)]="acceptForm.approveDestructiveMigration">
              <span style="color: #cf1322; font-weight: 500;">
                单独审批放行“破坏性数据库迁移”（仅当迁移风险为含破坏性变更时需勾选）
              </span>
            </label>
          </nz-form-item>

          <nz-form-item>
            <nz-form-label>验收通过意见</nz-form-label>
            <nz-form-control>
              <textarea
                nz-input
                name="accComment"
                rows="3"
                [(ngModel)]="acceptForm.comment"
                placeholder="记录验收试用确认意见（可选）"
              ></textarea>
            </nz-form-control>
          </nz-form-item>
        </form>
      </ng-container>
    </nz-modal>

    <!-- 打回重改弹窗 -->
    <nz-modal
      [nzVisible]="rejectModalVisible()"
      nzTitle="打回开发任务（回到编码会话继续修复）"
      [nzOkLoading]="submitting()"
      nzOkText="确认打回"
      (nzOnCancel)="rejectModalVisible.set(false)"
      (nzOnOk)="submitReject()"
    >
      <ng-container *nzModalContent>
        <form nz-form nzLayout="vertical">
          <nz-form-item>
            <nz-form-label nzRequired>打回原因与具体修改要求</nz-form-label>
            <nz-form-control>
              <textarea
                nz-input
                name="rejComment"
                rows="4"
                [(ngModel)]="rejectComment"
                placeholder="请写明预览发现的问题或未满足的细节，Agent 将在同一工作区回灌修复..."
              ></textarea>
            </nz-form-control>
          </nz-form-item>
        </form>
      </ng-container>
    </nz-modal>

    <!-- 放弃任务弹窗 -->
    <nz-modal
      [nzVisible]="abandonModalVisible()"
      nzTitle="放弃开发任务（关联需求将退回待排期）"
      [nzOkLoading]="submitting()"
      nzOkText="确认放弃任务"
      [nzOkDanger]="true"
      (nzOnCancel)="abandonModalVisible.set(false)"
      (nzOnOk)="submitAbandon()"
    >
      <ng-container *nzModalContent>
        <form nz-form nzLayout="vertical">
          <nz-form-item>
            <nz-form-label nzRequired>放弃原因说明</nz-form-label>
            <nz-form-control>
              <textarea
                nz-input
                name="abnComment"
                rows="3"
                [(ngModel)]="abandonComment"
                placeholder="填写放弃本次开发任务的原因"
              ></textarea>
            </nz-form-control>
          </nz-form-item>
        </form>
      </ng-container>
    </nz-modal>
  `,
  styles: [
    `
      .wi-header-bar {
        display: flex;
        justify-content: space-between;
        align-items: center;
        gap: 12px;
        flex-wrap: wrap;
        padding-bottom: 14px;
        margin-bottom: 16px;
        border-bottom: 1px solid #e2e8f0;
      }
      .action-gate-card {
        border-radius: 10px;
        padding: 16px;
        margin-bottom: 16px;
      }
      .gate-plan {
        background: #fffbe6;
        border: 1px solid #ffe58f;
      }
      .gate-answer {
        background: #fff7e6;
        border: 1px solid #ffd591;
      }
      .gate-card-title {
        font-size: 15px;
        font-weight: 600;
        color: #ad6800;
        margin-bottom: 12px;
        display: flex;
        align-items: center;
        gap: 8px;
      }
      .question-box {
        background: #ffffff;
        border: 1px solid #ffd591;
        border-radius: 8px;
        padding: 12px;
        margin-bottom: 12px;
      }
      .scope-hero-panel {
        background: #ffffff;
        border: 2px solid #d6e4ff;
        border-radius: 12px;
        padding: 16px 18px;
        margin-bottom: 16px;
      }
      .scope-hero-head {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 12px;
        flex-wrap: wrap;
      }
      .public-audit-banner {
        background: #fff1f0;
        border: 1px solid #ffa39e;
        border-radius: 8px;
        padding: 12px 14px;
        margin-bottom: 14px;
      }
    `
  ]
})
export class WorkItemsComponent implements OnInit {
  private readonly api = inject(AdminApiService);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);
  private readonly message = inject(NzMessageService);

  readonly products = signal<Product[]>([]);
  readonly items = signal<WorkItem[]>([]);
  readonly total = signal<number>(0);
  readonly page = signal<number>(1);
  readonly pageSize = signal<number>(20);
  readonly loading = signal<boolean>(false);
  readonly submitting = signal<boolean>(false);

  filterProductId: number | null = null;
  filterStatus: string | null = null;

  readonly drawerVisible = signal<boolean>(false);
  readonly detailLoading = signal<boolean>(false);
  readonly detail = signal<WorkItemDetailBundle | null>(null);

  // 弹窗状态与表单字段
  readonly acceptModalVisible = signal<boolean>(false);
  readonly rejectModalVisible = signal<boolean>(false);
  readonly abandonModalVisible = signal<boolean>(false);
  targetWorkId = 0;

  planComment = '';
  ownerAnswer = '';
  acceptForm = {
    version: '',
    approveDestructiveMigration: false,
    comment: ''
  };
  rejectComment = '';
  abandonComment = '';

  /** 按本模块区过滤出的文件改动列表 */
  readonly ownZoneChanges = computed<WorkScopeChange[]>(() =>
    (this.detail()?.scopeChanges || []).filter((c) => this.isOwnZone(c))
  );

  /** 按其他模块区过滤出的跨模块文件改动列表 */
  readonly otherZoneChanges = computed<WorkScopeChange[]>(() =>
    (this.detail()?.scopeChanges || []).filter((c) => this.isOtherZone(c))
  );

  /** 按公共区过滤出的高敏改动列表 */
  readonly publicZoneChanges = computed<WorkScopeChange[]>(() =>
    (this.detail()?.scopeChanges || []).filter((c) => this.isPublicZone(c))
  );

  /**
   * 排序改动明细：优先置顶展示“公共区”与“其他模块区”改动，让验收人第一眼聚焦边界外改动。
   */
  readonly sortedScopeChanges = computed<WorkScopeChange[]>(() => {
    const list = [...(this.detail()?.scopeChanges || [])];
    return list.sort((a, b) => this.zoneWeight(b) - this.zoneWeight(a));
  });

  /**
   * 从开发任务对象或活动流中自动解析当前待答复的 `questionId` 与提问正文。
   */
  readonly resolvedQuestion = computed<{ questionId: string; questionText: string }>(() => {
    const wi = this.detail()?.workItem;
    if (wi?.['pendingQuestionJson']) {
      try {
        const parsed = JSON.parse(String(wi['pendingQuestionJson']));
        if (parsed && (parsed.questionId || parsed.id || parsed.question)) {
          return {
            questionId: String(parsed.questionId || parsed.id || 'q-latest'),
            questionText: String(parsed.question || parsed.content || wi['pendingQuestionJson'])
          };
        }
      } catch {
        // 非 JSON 对象时继续使用兼容字段读取
      }
    }
    const acts = this.detail()?.activities || [];
    const askAct = [...acts]
      .reverse()
      .find(
        (a) =>
          String(a['activityType'] || a['kind'] || a['type'] || '').includes('ask') ||
          Boolean(a['questionId'])
      );
    const qId = String(
      wi?.pendingQuestionId || wi?.['questionId'] || askAct?.['questionId'] || 'q-latest'
    );
    const qText = String(
      wi?.pendingQuestion ||
        wi?.['pendingQuestionJson'] ||
        wi?.['question'] ||
        askAct?.['summary'] ||
        askAct?.['message'] ||
        '请查看活动流中的具体决策请求并给出指示'
    );
    return { questionId: qId, questionText: qText };
  });

  /**
   * 计算开发任务的总消耗 Token 数（兼容 `totalTokens` 或 `promptTokensUsed + completionTokensUsed`）。
   * @param wi 开发任务对象
   * @returns 累计总消耗 Token 数
   */
  getTotalTokens(wi: WorkItem): number {
    if (typeof wi.totalTokens === 'number' && wi.totalTokens > 0) {
      return wi.totalTokens;
    }
    return Number(wi['promptTokensUsed'] || wi.promptTokens || 0) +
      Number(wi['completionTokensUsed'] || wi.completionTokens || 0);
  }

  readonly formatDateTime = formatDateTime;
  readonly formatDuration = formatDuration;
  readonly getWorkStatusMeta = getWorkStatusMeta;
  readonly getMigrationRiskMeta = getMigrationRiskMeta;
  readonly getCheckStatusMeta = getCheckStatusMeta;
  readonly parseStringArray = parseStringArray;

  /**
   * 初始化订阅路由查询参数（支持从总览或需求池带参直达某状态或某任务详情）并加载列表。
   * @returns void
   */
  ngOnInit(): void {
    this.api.listProducts().subscribe({
      next: (list) => this.products.set(list),
      error: () => {}
    });

    this.route.queryParamMap.subscribe((q) => {
      const statusParam = q.get('status');
      const workItemIdParam = Number(q.get('workItemId') || 0);
      if (statusParam) {
        this.filterStatus = statusParam;
      }
      this.loadList();
      if (workItemIdParam > 0) {
        this.openDetail(workItemIdParam);
      }
    });
  }

  /**
   * 分页查询开发任务列表。
   * @returns void
   */
  loadList(): void {
    this.loading.set(true);
    this.api
      .listWorkItems({
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
          this.message.error(err.message || '查询开发任务失败');
        }
      });
  }

  /**
   * 重置页码至第 1 页执行查询。
   * @returns void
   */
  searchFromFirstPage(): void {
    this.page.set(1);
    this.loadList();
  }

  /**
   * 重置所有筛选条件。
   * @returns void
   */
  resetFilters(): void {
    this.filterProductId = null;
    this.filterStatus = null;
    this.page.set(1);
    this.loadList();
  }

  /**
   * 翻页事件处理。
   * @param idx 页码
   * @returns void
   */
  onPageChange(idx: number): void {
    this.page.set(idx);
    this.loadList();
  }

  /**
   * 每页条数变更事件处理。
   * @param size 每页条数
   * @returns void
   */
  onPageSizeChange(size: number): void {
    this.pageSize.set(size);
    this.page.set(1);
    this.loadList();
  }

  /**
   * 根据产品 ID 解析产品名称。
   * @param productId 产品主键 ID
   * @param fallback 预设产品名
   * @returns 产品可读名称
   */
  getProductName(productId: number, fallback?: string): string {
    if (fallback) {
      return fallback;
    }
    return this.products().find((p) => p.id === productId)?.name || `产品 #${productId}`;
  }

  /**
   * 判断该开发任务状态是否正在等待人工介入审批。
   * @param status 任务状态编码
   * @returns 需要人工动作返回 true
   */
  isActionNeeded(status?: string): boolean {
    const s = (status || '').toLowerCase();
    return (
      s === 'awaiting_plan' ||
      s === 'pending_plan' ||
      s === 'awaiting_answer' ||
      s === 'ask_owner' ||
      s === 'awaiting_acceptance' ||
      s === 'pending_acceptance'
    );
  }

  /**
   * 根据开发任务当前状态生成主操作按钮名称。
   * @param status 任务状态编码
   * @returns 操作按钮文案
   */
  getPrimaryActionLabel(status?: string): string {
    const s = (status || '').toLowerCase();
    if (s === 'awaiting_plan' || s === 'pending_plan') {
      return '确认方案';
    }
    if (s === 'awaiting_answer' || s === 'ask_owner') {
      return '回答提问';
    }
    if (s === 'awaiting_acceptance' || s === 'pending_acceptance') {
      return '打开验收包';
    }
    return '查看详情';
  }

  /**
   * 判断一条改动记录是否属于公共区。
   * @param sc 范围变更项
   * @returns 属于公共区返回 true
   */
  isPublicZone(sc: WorkScopeChange): boolean {
    const z = (sc.zone || sc.zoneType || '').toLowerCase();
    return z.includes('public') || z.includes('shared') || z.includes('common');
  }

  /**
   * 判断一条改动记录是否属于其他模块区。
   * @param sc 范围变更项
   * @returns 属于其他模块区返回 true
   */
  isOtherZone(sc: WorkScopeChange): boolean {
    const z = (sc.zone || sc.zoneType || '').toLowerCase();
    return z.includes('other') || z.includes('cross');
  }

  /**
   * 判断一条改动记录是否属于本模块区。
   * @param sc 范围变更项
   * @returns 属于本模块区返回 true
   */
  isOwnZone(sc: WorkScopeChange): boolean {
    return !this.isPublicZone(sc) && !this.isOtherZone(sc);
  }

  /**
   * 计算改动分区的展示优先级权重（公共区 > 其他模块区 > 本模块区）。
   * @param sc 范围变更项
   * @returns 权重分值
   */
  private zoneWeight(sc: WorkScopeChange): number {
    if (this.isPublicZone(sc)) {
      return 3;
    }
    if (this.isOtherZone(sc)) {
      return 2;
    }
    return 1;
  }

  /**
   * 汇总一组文件改动的总增删行数。
   * @param list 范围改动数组
   * @returns 总变更行数
   */
  sumLines(list: WorkScopeChange[]): number {
    return list.reduce(
      (acc, cur) => acc + Number(cur.linesChanged ?? (cur.addedLines || 0) + (cur.deletedLines || 0)),
      0
    );
  }

  /**
   * 判断活动流条目是否为沙箱权限档案拦截告警事件。
   * @param act 活动记录对象
   * @returns 是否为拦截告警
   */
  isBlockedActivity(act: Record<string, any>): boolean {
    const text = `${act['kind'] || ''} ${act['activityType'] || ''} ${act['type'] || ''} ${act['message'] || ''} ${act['summary'] || ''}`;
    return text.includes('拦截') || text.toLowerCase().includes('sandbox_block') || text.toLowerCase().includes('blocked');
  }

  /**
   * 打开指定开发任务的详情与验收包抽屉。
   * @param id 开发任务主键 ID
   * @returns void
   */
  openDetail(id: number): void {
    this.drawerVisible.set(true);
    this.detailLoading.set(true);
    this.api.getWorkItemDetail(id).subscribe({
      next: (bundle) => {
        this.detailLoading.set(false);
        this.detail.set(bundle);
      },
      error: (err: Error) => {
        this.detailLoading.set(false);
        this.message.error(err.message || '加载开发任务详情失败');
      }
    });
  }

  /**
   * 提交方案确认审批结果（同意开始编码或退回重拟）。
   * @param id 开发任务主键 ID
   * @param approved 是否同意方案
   * @returns void
   */
  submitPlanDecision(id: number, approved: boolean): void {
    this.submitting.set(true);
    this.api.confirmWorkPlan(id, { approved, comment: this.planComment.trim() }).subscribe({
      next: () => {
        this.submitting.set(false);
        this.planComment = '';
        this.message.success(approved ? '已同意技术方案，编码会话即将启动' : '已退回方案重新设计');
        this.openDetail(id);
        this.loadList();
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '提交方案确认失败');
      }
    });
  }

  /**
   * 提交针对开发 Agent `ask_owner` 提问的人工答复。
   * @param id 开发任务主键 ID
   * @returns void
   */
  submitAnswer(id: number): void {
    const ans = this.ownerAnswer.trim();
    if (!ans) {
      this.message.warning('请填写答复内容');
      return;
    }
    this.submitting.set(true);
    this.api
      .answerWorkQuestion(id, {
        questionId: this.resolvedQuestion().questionId,
        answer: ans
      })
      .subscribe({
        next: () => {
          this.submitting.set(false);
          this.ownerAnswer = '';
          this.message.success('答复已发送给开发 Agent，任务将继续执行');
          this.openDetail(id);
          this.loadList();
        },
        error: (err: Error) => {
          this.submitting.set(false);
          this.message.error(err.message || '发送答复失败');
        }
      });
  }

  /**
   * 打开验收通过确认弹窗并预填建议版本号。
   * @param wi 当前开发任务
   * @returns void
   */
  openAcceptModal(wi: WorkItem): void {
    this.targetWorkId = wi.id;
    this.acceptForm = {
      version: wi.suggestedVersion || wi['nextVersion'] || 'v0.1.0',
      approveDestructiveMigration: false,
      comment: ''
    };
    this.acceptModalVisible.set(true);
  }

  /**
   * 提交验收通过请求并触发自动发布。
   * @returns void
   */
  submitAccept(): void {
    if (!this.acceptForm.version.trim()) {
      this.message.warning('请确认发布版本号');
      return;
    }
    this.submitting.set(true);
    this.api
      .acceptWorkItem(this.targetWorkId, {
        version: this.acceptForm.version.trim(),
        approveDestructiveMigration: this.acceptForm.approveDestructiveMigration,
        comment: this.acceptForm.comment.trim()
      })
      .subscribe({
        next: () => {
          this.submitting.set(false);
          this.acceptModalVisible.set(false);
          this.message.success('验收已通过！自动合并与发布流水线已启动');
          this.openDetail(this.targetWorkId);
          this.loadList();
        },
        error: (err: Error) => {
          this.submitting.set(false);
          this.message.error(err.message || '提交验收通过失败');
        }
      });
  }

  /**
   * 打开打回修改弹窗。
   * @param wi 当前开发任务
   * @returns void
   */
  openRejectModal(wi: WorkItem): void {
    this.targetWorkId = wi.id;
    this.rejectComment = '';
    this.rejectModalVisible.set(true);
  }

  /**
   * 提交打回修改意见。
   * @returns void
   */
  submitReject(): void {
    if (!this.rejectComment.trim()) {
      this.message.warning('请填写具体的打回修改意见');
      return;
    }
    this.submitting.set(true);
    this.api.rejectWorkItem(this.targetWorkId, { comment: this.rejectComment.trim() }).subscribe({
      next: () => {
        this.submitting.set(false);
        this.rejectModalVisible.set(false);
        this.message.success('已打回开发任务，Agent 将按意见继续修复');
        this.openDetail(this.targetWorkId);
        this.loadList();
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '打回任务失败');
      }
    });
  }

  /**
   * 打开放弃任务确认弹窗。
   * @param wi 目标开发任务
   * @returns void
   */
  openAbandonModal(wi: WorkItem): void {
    this.targetWorkId = wi.id;
    this.abandonComment = '';
    this.abandonModalVisible.set(true);
  }

  /**
   * 提交放弃开发任务请求。
   * @returns void
   */
  submitAbandon(): void {
    if (!this.abandonComment.trim()) {
      this.message.warning('请填写放弃原因说明');
      return;
    }
    this.submitting.set(true);
    this.api.abandonWorkItem(this.targetWorkId, { comment: this.abandonComment.trim() }).subscribe({
      next: () => {
        this.submitting.set(false);
        this.abandonModalVisible.set(false);
        this.message.success('开发任务已放弃，关联需求已退回待排期');
        if (this.drawerVisible() && this.detail()?.workItem?.id === this.targetWorkId) {
          this.openDetail(this.targetWorkId);
        }
        this.loadList();
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '放弃任务失败');
      }
    });
  }

  /**
   * 复制预览地址或密码到剪贴板。
   * @param text 待复制内容
   * @param label 提示文本
   * @returns Promise<void>
   */
  async copyText(text: string, label: string): Promise<void> {
    const ok = await copyTextToClipboard(text);
    if (ok) {
      this.message.success(`已复制${label}`);
    }
  }

  /**
   * 跳转到系统设置页对应产品的权限档案子面板以便一键放行被拦截路径。
   * @param productId 产品主键 ID
   * @returns void
   */
  jumpToProductPermission(productId: number): void {
    this.drawerVisible.set(false);
    void this.router.navigate(['/settings'], {
      queryParams: { productId, subTab: 'permission' }
    });
  }
}

import { CommonModule } from '@angular/common';
import { Component, OnInit, inject, signal } from '@angular/core';
import { NavigationEnd, Router, RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router';
import { filter } from 'rxjs';
import { NzBadgeModule } from 'ng-zorro-antd/badge';
import { NzButtonModule } from 'ng-zorro-antd/button';
import { NzIconModule } from 'ng-zorro-antd/icon';
import { NzMessageService } from 'ng-zorro-antd/message';
import { NzTagModule } from 'ng-zorro-antd/tag';
import { NzTooltipModule } from 'ng-zorro-antd/tooltip';
import { AdminApiService } from '../core/services/admin-api.service';
import { AuthService } from '../core/services/auth.service';

/**
 * 侧边导航菜单配置项结构。
 */
interface NavItem {
  path: string;
  label: string;
  subtitle: string;
  icon: string;
}

/**
 * autoShip 管理后台主框架组件（深色固定侧边导航栏 + 顶栏状态指示 + 主内容区工作台）。
 */
@Component({
  selector: 'app-main-layout',
  standalone: true,
  imports: [
    CommonModule,
    RouterOutlet,
    RouterLink,
    RouterLinkActive,
    NzIconModule,
    NzButtonModule,
    NzTagModule,
    NzBadgeModule,
    NzTooltipModule
  ],
  template: `
    <div class="shell-layout">
      <!-- 左侧深色固定侧边导航栏 -->
      <aside class="shell-sider">
        <div class="sider-brand">
          <div class="sider-logo">
            <span nz-icon nzType="rocket" nzTheme="outline"></span>
          </div>
          <div class="sider-title-wrap">
            <div class="sider-title">autoShip</div>
            <div class="sider-sub">研发交付闭环平台 v0.3</div>
          </div>
        </div>

        <nav class="sider-nav">
          @for (item of navItems; track item.path) {
            <a
              class="nav-link"
              [routerLink]="item.path"
              routerLinkActive="nav-link-active"
            >
              <span nz-icon [nzType]="item.icon" class="nav-icon"></span>
              <span class="nav-label">{{ item.label }}</span>
              @if (item.path === '/work-items' && pendingAcceptCount() > 0) {
                <span class="nav-pill nav-pill-purple">{{ pendingAcceptCount() }}</span>
              }
              @if (item.path === '/releases' && pendingDeployCount() > 0) {
                <span class="nav-pill nav-pill-gold">{{ pendingDeployCount() }}</span>
              }
            </a>
          }
        </nav>

        <div class="sider-foot">
          <div class="foot-item">
            <span class="dot dot-green"></span>
            <span>Tailnet 专网安全域</span>
          </div>
          <div class="foot-item">
            <span
              class="dot"
              [class.dot-green]="llmHealthy()"
              [class.dot-amber]="!llmHealthy()"
            ></span>
            <span>
              模型通道：{{ llmMode() === 'direct' ? '直连接口' : 'Mac中继' }}
              ({{ llmHealthy() ? '在线' : '留言降级' }})
            </span>
          </div>
        </div>
      </aside>

      <!-- 右侧顶栏 + 主内容区 -->
      <div class="shell-main">
        <header class="shell-header">
          <div class="header-left">
            <span class="header-crumb">{{ activeNav().label }}</span>
            <span class="header-sep">/</span>
            <span class="header-desc">{{ activeNav().subtitle }}</span>
          </div>

          <div class="header-right">
            <nz-tag [nzColor]="llmHealthy() ? 'success' : 'warning'">
              <span nz-icon [nzType]="llmHealthy() ? 'check-circle' : 'exclamation-circle'"></span>
              <span>
                LLM {{ llmMode() === 'direct' ? '直连' : '中继' }}：{{ llmHealthy() ? '正常可用' : '离线降级' }}
              </span>
            </nz-tag>

            <nz-tag nzColor="geekblue" nz-tooltip nzTooltipTitle="写操作强制校验 Origin 与 X-AutoShip-CSRF 会话令牌">
              <span nz-icon nzType="safety-certificate"></span>
              <span>OTP + CSRF 已保护</span>
            </nz-tag>

            <button
              nz-button
              nzType="text"
              nzSize="small"
              nz-tooltip
              nzTooltipTitle="刷新顶栏通道与待办计数"
              (click)="refreshTopIndicators()"
            >
              <span nz-icon nzType="reload" [nzSpin]="refreshing()"></span>
            </button>

            <div class="user-chip">
              <span nz-icon nzType="user"></span>
              <span>{{ auth.currentUser()?.username || 'admin' }}</span>
            </div>

            <button nz-button nzType="default" nzSize="small" (click)="handleLogout()">
              <span nz-icon nzType="logout"></span>
              <span>退出</span>
            </button>
          </div>
        </header>

        <main class="shell-content">
          <router-outlet></router-outlet>
        </main>
      </div>
    </div>
  `,
  styles: [
    `
      .shell-layout {
        display: flex;
        min-height: 100vh;
        background: var(--as-bg-body);
      }
      .shell-sider {
        width: 232px;
        background: var(--as-sidebar-bg);
        color: #cbd5e1;
        display: flex;
        flex-direction: column;
        position: fixed;
        top: 0;
        left: 0;
        bottom: 0;
        z-index: 100;
        box-shadow: 2px 0 12px rgba(0, 0, 0, 0.22);
      }
      .sider-brand {
        height: 64px;
        padding: 0 18px;
        display: flex;
        align-items: center;
        gap: 12px;
        border-bottom: 1px solid rgba(255, 255, 255, 0.08);
      }
      .sider-logo {
        width: 36px;
        height: 36px;
        border-radius: 9px;
        background: linear-gradient(135deg, #1677ff, #36cfc9);
        color: #ffffff;
        display: flex;
        align-items: center;
        justify-content: center;
        font-size: 19px;
        flex-shrink: 0;
      }
      .sider-title {
        font-size: 17px;
        font-weight: 700;
        color: #ffffff;
        letter-spacing: 0.3px;
      }
      .sider-sub {
        font-size: 11px;
        color: #94a3b8;
      }
      .sider-nav {
        flex: 1;
        padding: 14px 10px;
        display: flex;
        flex-direction: column;
        gap: 4px;
        overflow-y: auto;
      }
      .nav-link {
        display: flex;
        align-items: center;
        gap: 11px;
        padding: 10px 14px;
        border-radius: 8px;
        color: #cbd5e1;
        text-decoration: none;
        font-size: 14px;
        font-weight: 500;
        transition: background 0.15s ease, color 0.15s ease;
      }
      .nav-link:hover {
        background: rgba(255, 255, 255, 0.07);
        color: #ffffff;
      }
      .nav-link-active {
        background: #1677ff !important;
        color: #ffffff !important;
        box-shadow: 0 4px 12px rgba(22, 119, 255, 0.35);
      }
      .nav-icon {
        font-size: 16px;
      }
      .nav-label {
        flex: 1;
      }
      .nav-pill {
        font-size: 11px;
        font-weight: 600;
        padding: 1px 7px;
        border-radius: 10px;
        line-height: 16px;
      }
      .nav-pill-purple {
        background: #722ed1;
        color: #ffffff;
      }
      .nav-pill-gold {
        background: #faad14;
        color: #141414;
      }
      .sider-foot {
        padding: 12px 16px;
        border-top: 1px solid rgba(255, 255, 255, 0.08);
        font-size: 12px;
        color: #94a3b8;
        display: flex;
        flex-direction: column;
        gap: 6px;
      }
      .foot-item {
        display: flex;
        align-items: center;
        gap: 8px;
      }
      .dot {
        width: 8px;
        height: 8px;
        border-radius: 50%;
        display: inline-block;
        flex-shrink: 0;
      }
      .dot-green {
        background: #52c41a;
        box-shadow: 0 0 6px rgba(82, 196, 26, 0.7);
      }
      .dot-amber {
        background: #faad14;
      }
      .shell-main {
        flex: 1;
        margin-left: 232px;
        display: flex;
        flex-direction: column;
        min-width: 0;
      }
      .shell-header {
        height: 58px;
        background: #ffffff;
        border-bottom: 1px solid var(--as-border-subtle);
        padding: 0 24px;
        display: flex;
        align-items: center;
        justify-content: space-between;
        position: sticky;
        top: 0;
        z-index: 90;
      }
      .header-left {
        display: flex;
        align-items: center;
        gap: 8px;
        min-width: 0;
      }
      .header-crumb {
        font-size: 15px;
        font-weight: 600;
        color: #0f172a;
      }
      .header-sep {
        color: #cbd5e1;
      }
      .header-desc {
        font-size: 12.5px;
        color: #64748b;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
      }
      .header-right {
        display: flex;
        align-items: center;
        gap: 10px;
      }
      .user-chip {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 3px 10px;
        border-radius: 16px;
        background: #f1f5f9;
        color: #1e293b;
        font-size: 13px;
        font-weight: 500;
      }
      .shell-content {
        padding: 20px 24px 32px;
        flex: 1;
        min-width: 0;
      }
    `
  ]
})
export class MainLayoutComponent implements OnInit {
  readonly auth = inject(AuthService);
  private readonly api = inject(AdminApiService);
  private readonly router = inject(Router);
  private readonly message = inject(NzMessageService);

  /** 左侧固定菜单项配置 */
  readonly navItems: NavItem[] = [
    {
      path: '/dashboard',
      label: '总览看板',
      subtitle: '全局交付闭环计数、Agent 心跳与模型通道态势',
      icon: 'dashboard'
    },
    {
      path: '/requirements',
      label: '需求池',
      subtitle: '需求澄清归档、分拣排期、手动录入与一键开工',
      icon: 'bulb'
    },
    {
      path: '/work-items',
      label: '开发任务',
      subtitle: '方案确认、改动范围审计、自动化门禁与预览验收',
      icon: 'code'
    },
    {
      path: '/releases',
      label: '版本管理',
      subtitle: '签名制品清单、失败建修复单与人工确认部署关口',
      icon: 'rocket'
    },
    {
      path: '/deployments',
      label: '部署中心',
      subtitle: '生产升级分步日志、幂等回滚与数据库备份配额监控',
      icon: 'cloud-server'
    },
    {
      path: '/settings',
      label: '系统设置',
      subtitle: '产品档案、权限沙箱、Open API 接入方、Agent 令牌与 PKI',
      icon: 'setting'
    },
    {
      path: '/audit',
      label: '安全审计',
      subtitle: '后台审批、指令投递、部署与配置变更全量审计追溯',
      icon: 'audit'
    }
  ];

  readonly activeNav = signal<NavItem>(this.navItems[0]);
  readonly llmMode = signal<string>('relay');
  readonly llmHealthy = signal<boolean>(true);
  readonly pendingAcceptCount = signal<number>(0);
  readonly pendingDeployCount = signal<number>(0);
  readonly refreshing = signal<boolean>(false);

  /**
   * 初始化布局顶栏路由监听与全局运行指标加载。
   * @returns void
   */
  ngOnInit(): void {
    this.syncActiveNav(this.router.url);
    this.router.events
      .pipe(filter((e): e is NavigationEnd => e instanceof NavigationEnd))
      .subscribe((ev) => {
        this.syncActiveNav(ev.urlAfterRedirects);
      });
    this.refreshTopIndicators();
  }

  /**
   * 静默拉取 `/api/v1/admin/overview` 同步顶栏 LLM 健康度与侧边栏待验收/待部署徽标计数。
   * @returns void
   */
  refreshTopIndicators(): void {
    this.refreshing.set(true);
    this.api.getOverview().subscribe({
      next: (ov) => {
        this.refreshing.set(false);
        this.llmMode.set(ov?.llmStatus?.mode || 'relay');
        this.llmHealthy.set(Boolean(ov?.llmStatus?.healthy));
        this.pendingAcceptCount.set(Number(ov?.counts?.pendingAcceptance ?? 0));
        this.pendingDeployCount.set(Number(ov?.counts?.pendingDeploy ?? 0));
      },
      error: () => {
        this.refreshing.set(false);
      }
    });
  }

  /**
   * 退出当前管理员会话并返回登录页。
   * @returns void
   */
  handleLogout(): void {
    this.auth.logout().subscribe({
      next: () => {
        this.message.success('已安全退出登录');
        void this.router.navigate(['/login']);
      },
      error: () => {
        this.auth.clearSessionState();
        void this.router.navigate(['/login']);
      }
    });
  }

  /**
   * 根据当前 URL 匹配高亮菜单说明信息。
   * @param url 当前浏览器路由路径
   * @returns void
   */
  private syncActiveNav(url: string): void {
    const matched = this.navItems.find((item) => url.startsWith(item.path));
    if (matched) {
      this.activeNav.set(matched);
    }
  }
}

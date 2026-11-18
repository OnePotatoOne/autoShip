import { Routes } from '@angular/router';
import { adminAuthGuard } from './core/guards/auth.guard';
import { MainLayoutComponent } from './layout/main-layout.component';

/**
 * autoShip 管理后台顶层路由表定义。
 */
export const appRoutes: Routes = [
  {
    path: 'login',
    loadComponent: () =>
      import('./pages/login/login.component').then((m) => m.LoginComponent)
  },
  {
    path: '',
    component: MainLayoutComponent,
    canActivate: [adminAuthGuard],
    children: [
      { path: '', pathMatch: 'full', redirectTo: 'dashboard' },
      {
        path: 'dashboard',
        loadComponent: () =>
          import('./pages/dashboard/dashboard.component').then((m) => m.DashboardComponent)
      },
      {
        path: 'requirements',
        loadComponent: () =>
          import('./pages/requirements/requirements.component').then(
            (m) => m.RequirementsComponent
          )
      },
      {
        path: 'work-items',
        loadComponent: () =>
          import('./pages/work-items/work-items.component').then((m) => m.WorkItemsComponent)
      },
      {
        path: 'releases',
        loadComponent: () =>
          import('./pages/releases/releases.component').then((m) => m.ReleasesComponent)
      },
      {
        path: 'deployments',
        loadComponent: () =>
          import('./pages/deployments/deployments.component').then(
            (m) => m.DeploymentsComponent
          )
      },
      {
        path: 'settings',
        loadComponent: () =>
          import('./pages/settings/settings.component').then((m) => m.SettingsComponent)
      },
      {
        path: 'audit',
        loadComponent: () =>
          import('./pages/audit/audit.component').then((m) => m.AuditComponent)
      }
    ]
  },
  { path: '**', redirectTo: 'dashboard' }
];

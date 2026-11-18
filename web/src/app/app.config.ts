import { ApplicationConfig, provideZoneChangeDetection } from '@angular/core';
import { provideRouter } from '@angular/router';
import { provideHttpClient, withInterceptors } from '@angular/common/http';
import { provideAnimations } from '@angular/platform-browser/animations';
import { provideNzI18n, zh_CN } from 'ng-zorro-antd/i18n';
import { provideNzIcons } from 'ng-zorro-antd/icon';
import { IconDefinition } from '@ant-design/icons-angular';
import {
  AlertOutline,
  ApiOutline,
  AuditOutline,
  BranchesOutline,
  BulbOutline,
  CheckCircleOutline,
  ClockCircleOutline,
  CloseCircleOutline,
  CloudServerOutline,
  ClusterOutline,
  CodeOutline,
  CopyOutline,
  DashboardOutline,
  DatabaseOutline,
  DeleteOutline,
  DownloadOutline,
  EditOutline,
  ExclamationCircleOutline,
  EyeOutline,
  FileTextOutline,
  KeyOutline,
  LinkOutline,
  LockOutline,
  LogoutOutline,
  PlayCircleOutline,
  PlusOutline,
  ReloadOutline,
  RobotOutline,
  RocketOutline,
  RollbackOutline,
  SafetyCertificateOutline,
  SafetyOutline,
  SearchOutline,
  SendOutline,
  SettingOutline,
  SyncOutline,
  ThunderboltOutline,
  ToolOutline,
  UserOutline
} from '@ant-design/icons-angular/icons';
import { appRoutes } from './app.routes';
import { authCsrfInterceptor } from './core/interceptors/auth-csrf.interceptor';

/**
 * 全局注册的 Ant Design 图标清单，确保离线内嵌部署下零外部请求。
 */
const AUTOSHIP_ICONS: IconDefinition[] = [
  DashboardOutline,
  BulbOutline,
  CodeOutline,
  RocketOutline,
  CloudServerOutline,
  SettingOutline,
  SafetyCertificateOutline,
  SafetyOutline,
  AuditOutline,
  PlusOutline,
  ReloadOutline,
  SearchOutline,
  CheckCircleOutline,
  CloseCircleOutline,
  ExclamationCircleOutline,
  ClockCircleOutline,
  SyncOutline,
  LogoutOutline,
  LockOutline,
  UserOutline,
  KeyOutline,
  CopyOutline,
  EyeOutline,
  EditOutline,
  DeleteOutline,
  PlayCircleOutline,
  RollbackOutline,
  DatabaseOutline,
  BranchesOutline,
  LinkOutline,
  SendOutline,
  DownloadOutline,
  ApiOutline,
  RobotOutline,
  AlertOutline,
  FileTextOutline,
  ThunderboltOutline,
  ClusterOutline,
  ToolOutline
];

/**
 * autoShip Angular 21 全局应用级配置对象。
 */
export const appConfig: ApplicationConfig = {
  providers: [
    provideZoneChangeDetection({ eventCoalescing: true }),
    provideRouter(appRoutes),
    provideHttpClient(withInterceptors([authCsrfInterceptor])),
    provideAnimations(),
    provideNzI18n(zh_CN),
    provideNzIcons(AUTOSHIP_ICONS)
  ]
};

import { registerLocaleData } from '@angular/common';
import zh from '@angular/common/locales/zh';
import { bootstrapApplication } from '@angular/platform-browser';
import { AppComponent } from './app/app.component';
import { appConfig } from './app/app.config';

// 注册简体中文区域格式化支持
registerLocaleData(zh);

/**
 * 启动 autoShip 管理后台单页前端应用。
 */
bootstrapApplication(AppComponent, appConfig).catch((err) => {
  console.error('autoShip 前端启动失败:', err);
});

import { Component } from '@angular/core';
import { RouterOutlet } from '@angular/router';

/**
 * autoShip 管理后台根组件，承载顶层路由视图容器。
 */
@Component({
  selector: 'app-root',
  standalone: true,
  imports: [RouterOutlet],
  template: `<router-outlet></router-outlet>`
})
export class AppComponent {}

import { NgModule } from '@angular/core'
import { RouterModule, Routes } from '@angular/router'
import { UpstreamDetailComponent } from './upstream-detail/upstream-detail.component'
import { IntelligentPluginComponent } from './intelligent-plugin.component'
import { IntelligentPluginLayoutComponent } from './layout/layout.component'

const routes: Routes = [{
  path: '',
  component: IntelligentPluginComponent,
  children: [
    { path: 'upstream/detail/:uuid', component: UpstreamDetailComponent },
    {
      path: ':moduleName',
      children: [{
        path: '**',
        component: IntelligentPluginLayoutComponent
      }
      ]
    }
  ]
}]

@NgModule({
  imports: [RouterModule.forChild(routes)],
  exports: [RouterModule]
})
export class IntelligentPluginRoutingModule { }

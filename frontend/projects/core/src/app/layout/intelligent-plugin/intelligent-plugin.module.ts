import { UpstreamDetailComponent } from './upstream-detail/upstream-detail.component'
import { EoNgTabsModule } from 'eo-ng-tabs'
import { EoNgRadioModule } from 'eo-ng-radio'
import { EoNgCheckboxModule } from 'eo-ng-checkbox'
import { NzFormModule } from 'ng-zorro-antd/form'
import { NzEmptyModule } from 'ng-zorro-antd/empty'
import { NzModalModule } from 'ng-zorro-antd/modal'
import { NgModule } from '@angular/core'
import { CommonModule } from '@angular/common'
import { IntelligentPluginRoutingModule } from './intelligent-plugin-routing.module'
import { IntelligentPluginCreateComponent } from './create/create.component'
import { IntelligentPluginLayoutComponent } from './layout/layout.component'
import { IntelligentPluginListComponent } from './list/list.component'
import { IntelligentPluginPublishComponent } from './publish/publish.component'
import { ComponentModule } from '../../component/component.module'
import { EoNgButtonModule } from 'eo-ng-button'
import { EoNgSelectModule } from 'eo-ng-select'
import { EoNgInputModule } from 'eo-ng-input'
import { EoNgApintoTableModule } from 'projects/eo-ng-apinto-table/src/public-api'
import { FormsModule, ReactiveFormsModule } from '@angular/forms'
import { DirectiveModule } from '../../directive/directive.module'
import { NzSpinModule } from 'ng-zorro-antd/spin'

const sharedEoLibraryModules = [
  EoNgButtonModule,
  EoNgSelectModule,
  EoNgInputModule,
  EoNgApintoTableModule
]
@NgModule({
  declarations: [
    UpstreamDetailComponent,
    IntelligentPluginLayoutComponent,
    IntelligentPluginListComponent,
    IntelligentPluginPublishComponent,
    IntelligentPluginCreateComponent
  ],
  imports: [
    EoNgTabsModule, EoNgRadioModule, EoNgCheckboxModule, NzFormModule, NzEmptyModule, NzModalModule,
    CommonModule,
    IntelligentPluginRoutingModule,
    DirectiveModule,
    FormsModule,
    ReactiveFormsModule,
    ComponentModule,
    ...sharedEoLibraryModules,
    NzSpinModule
  ]
})
export class IntelligentPluginModule {
}

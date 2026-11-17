import { Component, OnInit, ViewChild } from '@angular/core'
import { THEAD_TYPE } from 'eo-ng-table'
import { EO_TBODY_TYPE } from 'projects/eo-ng-apinto-table/src/public-api'
import { formatDate } from '@angular/common'
import { ActivatedRoute, Router } from '@angular/router'
import { HttpClient } from '@angular/common/http'
import { firstValueFrom, forkJoin } from 'rxjs'
import { EoNgNavigationService } from '../../../service/eo-ng-navigation.service'
import { ApiService } from '../../../service/api.service'
import { CustomReactComponentWrapperComponent } from '../../../component/formily2-react/CustomReactComponentWrapper'
import { EoNgFeedbackMessageService } from 'eo-ng-feedback'
import { EoIntelligentPluginService } from '../../../component/intelligent-plugin/intelligent-plugin.service'

interface ContractAPI { source:string; auto_configure:boolean; rpc_service:string; rpc_method:string; uuid:string; name:string; contract:string; file:string; removed:boolean; method:string[]; request_path:string; is_disable:boolean }
interface ContractInfo {
  include_unannotated:boolean; version_id:number; revision:string; files:Record<string,string>; apis:ContractAPI[];
  versions:Array<{id:number; revision:string; create_time:string}>
}
@Component({
  selector: 'eo-upstream-detail',
  templateUrl: './upstream-detail.component.html',
  styleUrls: ['./upstream-detail.component.scss']
})
export class UpstreamDetailComponent implements OnInit {
  @ViewChild('settings') settings?:CustomReactComponentWrapperComponent
  name = ''
  title = ''
  tab = 0
  tabs = [{title:'上游设置'}, {title:'契约管理'}]
  updateModes = [{label:'解析链接',value:'url'}, {label:'上传文件',value:'upload'}]
  versionOptions:Array<{label:string;value:number}> = []
  clusterOptions:Array<{label:string;value:string}> = []
  apiRows:Array<ContractAPI & {methodText:string;pathText:string;statusText:string}> = []
  apiColumns:THEAD_TYPE[] = [{title:'接口'}, {title:'契约'}, {title:'方法',width:100}, {title:'路径'}, {title:'自动配置 API',width:130}, {title:'操作',right:true}]
  apiCells:EO_TBODY_TYPE[] = [
    {key:'name'}, {key:'contract'}, {key:'methodText'}, {key:'pathText',copy:true}, {key:'statusText'},
    {type:'btn',right:true,btns:[{title:'配置',showFn:(item:ContractAPI) => !!item.uuid || !item.removed && !this.historical,disabledFn:() => this.busy,click:(item:{data:ContractAPI}) => this.configureAPI(item.data)}]}
  ]
  ready = false
  schema:any = {}
  values:any = {}
  drivers:any[] = []
  contract:ContractInfo = { include_unannotated:false, version_id:0, revision:'', files:{}, apis:[], versions:[] }
  currentVersion = 0
  selectedVersion = 0
  selectedPath = ''
  content = ''
  error = ''
  busy = false
  updateOpen = false
  mode = 'url'
  includeUnannotated = false
  url = ''
  file:File|null = null
  manualOpen = false
  manualAPI:ContractAPI|null = null
  manualMethod = 'POST'
  manualPath = ''
  httpMethods = ['GET','POST','PUT','DELETE','PATCH','HEAD','OPTIONS'].map(value => ({label:value,value}))
  publishOpen = false
  clusters:Array<{name:string; title:string}> = []
  selectedClusters:string[] = []
  publishResults:Array<{APIName:string; ClusterEnv:string; Status:boolean; Result:string}> = []

  constructor (private route:ActivatedRoute, public router:Router, private api:ApiService,
    private http:HttpClient, private navigation:EoNgNavigationService, private message:EoNgFeedbackMessageService, private plugins:EoIntelligentPluginService) {}

  ngOnInit ():void {
    this.name = this.route.snapshot.paramMap.get('uuid') || ''
    this.selectTab(0)
    forkJoin([
      this.api.get('dynamic/upstream/render'),
      this.api.get(`dynamic/upstream/info/${encodeURIComponent(this.name)}`),
      this.api.get('dynamic/upstream/list', {page:1,page_size:1}),
      this.api.get('clusters/simple')
    ]).subscribe({next:([render,info,list,clusters]:any[]) => {
      if ([render,info,list,clusters].some(r => r.code !== 0)) {
        this.error = [render,info,list,clusters].find(r => r.code !== 0)?.msg || '加载失败'; return
      }
      this.schema = render.data.render
      this.values = info.data
      this.title = info.data.title || this.name
      this.drivers = list.data.drivers.map((d:any) => ({label:d.title,value:d.name}))
      this.clusters = clusters.data.clusters
      this.clusterOptions = this.clusters.map(c => ({label:c.title,value:c.name}))
      this.ready = true
    },error:() => { this.error = '上游加载失败，请刷新重试' }})
    this.loadContract()
  }
  selectTab (index:number):void {
    this.tab = index
    this.navigation.reqFlashBreadcrumb([{title:'上游',routerLink:'template/upstream'}, {title:index === 0 ? '上游设置' : '契约管理'}])
  }
  get paths ():string[] { return Object.keys(this.contract.files).sort() }
  get historical ():boolean { return this.selectedVersion !== this.currentVersion }
  get dirty ():boolean { return !!this.selectedPath && this.content !== this.contract.files[this.selectedPath] }
  private endpoint (suffix = ''):string { return this.api.urlPrefix + 'api/upstream/' + encodeURIComponent(this.name) + '/contract' + suffix }
  async request (method:string,suffix:string,body?:any):Promise<any> {
    const response:any = await firstValueFrom(this.http.request(method,this.endpoint(suffix),{body,params:{namespace:'default'}}))
    if (response.code !== 0) throw new Error(response.msg || '操作失败')
    return response.data
  }
  async loadContract (version = 0):Promise<void> {
    try {
      const data = await this.request('GET',version ? `?version=${version}` : '')
      this.applyContract(data)
    } catch (e:any) { this.error = e.message || '契约加载失败' }
  }
  applyContract (data:ContractInfo):void {
    this.contract = data
    this.apiRows = data.apis.map(item => ({...item,methodText:item.method.join(', ') || '—',pathText:item.request_path || '—',statusText:item.removed ? '契约已移除' : item.auto_configure ? '启用' : '禁用'}))
    this.versionOptions = data.versions.map(v => ({value:v.id,label:`#${v.id} · ${formatDate(v.create_time,'yyyy-MM-dd HH:mm:ss','en-US')}`}))
    this.currentVersion = data.versions[0]?.id || 0
    this.selectedVersion = data.version_id
    if (!data.files[this.selectedPath]) this.selectedPath = Object.keys(data.files).sort()[0] || ''
    this.content = data.files[this.selectedPath] || ''
    this.error = ''
    this.publishResults = []
  }
  configureAPI (item:ContractAPI):void {
    if (item.uuid && (item.source === 'manual' || item.auto_configure || item.removed || this.historical)) { this.router.navigate(['/router/api/message',item.uuid]); return }
    if (this.historical || item.removed || this.busy) return
    this.manualAPI=item; this.manualMethod='POST'; this.manualPath=''; this.error=''; this.manualOpen=true
  }
  async createManualAPI ():Promise<void> {
    await this.run(async () => {
      const item=this.manualAPI
      if (!item || this.historical) return
      if (!this.manualPath.trim().startsWith('/')) throw new Error('请输入以 / 开头的 HTTP 路径')
      const data:ContractInfo=await this.request('POST','/api',{
        base_version:this.currentVersion,service:item.rpc_service,method:item.rpc_method,file:item.file,
        http_method:this.manualMethod,path:this.manualPath.trim()
      })
      this.applyContract(data)
      this.manualOpen=false
      const created=data.apis.find(api => api.rpc_service===item.rpc_service && api.rpc_method===item.rpc_method && api.file===item.file)
      this.message.success('API 草稿已保存并关联契约，请继续配置，发布后生效')
      if (created?.uuid) await this.router.navigate(['/router/api/message',created.uuid])
    })
  }
  async run (work:()=>Promise<void>):Promise<void> {
    if (this.busy) return
    this.busy = true; this.error = ''
    try { await work() } catch (e:any) { this.error = e.message || '操作失败' } finally { this.busy = false }
  }
  async saveSettings ():Promise<void> {
    await this.run(async () => {
      const form = this.settings?.reactComponent.current.form
      if (!form) throw new Error('表单尚未加载')
      await form.validate()
      const response:any = await firstValueFrom(this.api.put(`dynamic/upstream/config/${encodeURIComponent(this.name)}`,JSON.parse(JSON.stringify(form.values))))
      if (response.code !== 0) throw new Error(response.msg)
      this.title = form.values.title || this.name
      this.message.success('上游设置已保存，请通过发布管理发布')
    })
  }
  publishSettings ():void { this.plugins.publishPluginModal('upstream',{name:this.title,id:this.name}) }
  chooseFile (event:Event):void { this.file = (event.target as HTMLInputElement).files?.[0] || null }
  openUpdate ():void { this.mode='url'; this.includeUnannotated=false; this.url=''; this.file=null; this.updateOpen=true }
  async update ():Promise<void> {
    await this.run(async () => {
      let body:any
      if (this.mode === 'url') {
        if (!this.url.trim()) throw new Error('请输入 ZIP 下载链接')
        body = {url:this.url.trim(),base_version:this.currentVersion,include_unannotated:this.includeUnannotated}
      } else {
        if (!this.file) throw new Error('请选择 ZIP 文件')
        if (this.file.size > 16*1024*1024) throw new Error('ZIP 文件不能超过 16 MiB')
        body = new FormData(); body.append('file',this.file); body.append('base_version',String(this.currentVersion)); body.append('include_unannotated',String(this.includeUnannotated))
      }
      this.applyContract(await this.request('POST','',body))
      this.updateOpen=false; this.url=''; this.file=null
      this.message.success('契约已更新，API 草稿已同步，发布后生效')
    })
  }
  selectPath (path:string):void {
    if (this.dirty && !window.confirm('当前文件尚未保存，是否放弃修改？')) return
    this.selectedPath=path; this.content=this.contract.files[path]
  }
  async changeVersion (version:number):Promise<void> {
    if (this.dirty && !window.confirm('当前文件尚未保存，是否放弃修改？')) return
    await this.loadContract(Number(version))
  }
  async saveFile ():Promise<void> {
    await this.run(async () => {
      this.applyContract(await this.request('PUT','/file',{path:this.selectedPath,content:this.content,base_version:this.currentVersion}))
      this.message.success('文件已保存为新契约版本，发布后生效')
    })
  }
  async restore ():Promise<void> {
    await this.run(async () => {
      this.applyContract(await this.request('POST','/restore',{version:this.selectedVersion,base_version:this.currentVersion}))
      this.message.success('历史版本已恢复到草稿，发布后生效')
    })
  }
  async publish ():Promise<void> {
    await this.run(async () => {
      if (!this.selectedClusters.length) throw new Error('请选择集群')
      this.publishResults = await this.request('POST','/publish',{version:this.currentVersion,clusters:this.selectedClusters})
      if (this.publishResults.some(r => !r.Status)) {
        this.error='部分 API 发布失败，请查看下方结果，修正后重新发布'; return
      }
      this.publishOpen=false; this.message.success('契约 API 已发布')
    })
  }
}

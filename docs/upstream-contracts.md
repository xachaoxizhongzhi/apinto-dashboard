# 上游契约管理

在上游列表点击编辑，进入「上游设置 / 契约管理」。设置页保留原动态表单和发布管理。

## 使用流程

1. 点击「更新契约」，选择 HTTP/HTTPS ZIP 链接或上传 ZIP。
2. 查看文件、在线编辑、检查生成的 API 草稿。历史版本可恢复为新草稿。
3. 发布上游及所需节点插件，再通过「发布契约」发布关联 API。保存与恢复不会自动发布。

ZIP 可以包含多个 `.thrift` 文件及目录，所有 include 必须在包内。限制为 ZIP 16 MiB、解压内容 32 MiB、256 个文件。非法路径、非 Thrift 文件、缺失依赖、循环 include 和非法语法会拒绝整次更新。下载链接不保存。

方法的 HTTP 注解生成路由，方法注释用作 API 名称和描述；没有 HTTP 注解的方法不自动生成 API，可以在方法列表中点击“配置”，填写 HTTP 方法和请求路径，创建关联草稿后进入原 API 编辑页。更新契约时可勾选“解析无 HTTP 注解的方法”（默认不勾选），勾选后才把这些方法列入契约管理列表。此选项按契约版本保存，在线编辑和恢复历史版本沿用对应版本的设置。

```thrift
struct GetReq { 1: optional string id (api.query = 'id') }
struct GetRes { 1: optional string name }
service Catalog {
    // 查询商品
    GetRes Get(1: GetReq request) (
        api.get = '/catalog/get'
        api.router.header.appcode.key = 'x-app-code'
        api.router.header.appcode.value = 'catalog'
        api.header.map.userId = 'x-user-id'
    )
}
```

`api.auth.*` 使用参数校验插件实例，`api.permission.*` 使用 `eolinker.com:apinto:user_permissions` 驱动，`api.response.wrap` 使用 `response_wrap`。引用的插件须先在节点插件中注册。发布前要求 `http_to_kitex` 在目标集群以普通启用状态发布。

## 存储与版本

保留原 MySQL 和原版版本/历史表字段。契约相关的三张表：

- `api_contract_artifact`：以工作空间 + 文件内容 SHA256 唯一存一份完整契约。相同内容重复上传不重复存储。
- `api_contract_version`：引用契约资源和原有 API 版本 ID，保留上游的全量更新记录；不复制 IDL 或完整 API 配置。已有版本编号保持不变。
- `api_contract_binding`：上游内部 ID + Service + Method 对应稳定 API UUID，并记录自动（auto）或手动（manual）来源；来源也随契约版本快照保存。

`http_to_kitex` 保存 `contract`、`main_idl`、`idl_service`、`service`、`method` 和 `header_mapping`。路由上的 `contracts` 声明 Apinto 的资源依赖；文件移动不改变 API UUID。完整契约通过独立 `transcode/thrift` 资源下发一次，名称 `thrift-<sha256>`。网关本地持久化资源，不从 Dashboard 或临时 ZIP 链接在请求时下载。

每个网关进程内，同一契约、入口文件、IDL Service 和目标服务共享解析结果和 Kitex 客户端；方法只保存引用和自己的头映射。不同节点各自保存一份是正常的副本。资源不可原地改内容，在线引用阻止删除；本版不自动清理历史资源，以保证回滚和在途请求。

全量更新生成草稿：更新已有 API、新建新方法，将消失的方法置为禁用；未关联契约的独立 API 不被接管。手工插件、超时与非契约路由条件保留。恢复历史契约会恢复当时保存的精确 API 版本配置，新增于目标版本之后的 API 置为禁用草稿；恢复后仍需发布才生效。

契约发布使用该契约快照中固定的 API 版本，先在目标集群准备所有契约资源，再走原来的逐条 API 发布流程。资源准备失败不发路由；超时会回读资源版本确认。普通单条和批量 API 发布同样先准备引用的契约资源。契约生成后的手工 API 修改可通过原有 API 发布入口发布；“发布契约”发布的是契约快照，不会偷偷换成更新后的草稿。

仍是原有的逐 API、逐集群发布，不承诺整批原子切换。部分失败按原结果逐项重试。历史资源不会因新版本发布而删除；上游、服务发现、全局插件不随契约回滚。

## 旧数据升级

旧版把完整 IDL 写进 API、操作历史和发布历史，升级前须先迁移。新网关不接受内嵌 `thrift_files` 的旧路由格式；不要直接覆盖运行中的旧网关。

1. 停止 Dashboard 写入，备份网关现有配置。
2. 使用现有数据库连接串通过环境变量 `CONTRACT_MYSQL_DSN` 提供给迁移工具，执行：

   ```sh
   go run ./app/contract-migrate -backup /安全目录/contracts-before-migration.json.gz
   ```

   工具先创建权限 `0600` 的备份，再按内容去重迁移历史文件；API UUID、既有 API 版本 ID、契约版本编号不变。原有契约快照转换为原生 API 版本引用。迁移不调用网关发布接口，可重复执行，失败时当前行事务回滚。
3. 确认迁移成功后启动新版 Dashboard。此前为了内嵌大契约扩大为 `LONGTEXT` 的源码改动已撤销；不要在未迁移旧数据时运行原生表结构同步。
4. 在升级后的网关中先准备契约资源，再重新发布相应 API；原生旧格式路由需要重新发布为引用格式。应先在测试节点验证，再切换流量。

## 验证

```sh
GOTOOLCHAIN=go1.24.6 go test -tags mysql ./modules/api/contract ./modules/api/service ./modules/api/controller
```

设置 `TEST_CONTRACT_MYSQL_DSN` 可在空库或已完成上述迁移的开发库运行集成测试。测试业务数据在事务结束时回滚；存储初始化会检查及同步当前模型表结构。不会发布到网关。

## 手动关联 API

“自动配置 API”的启用/禁用只表示是否由 HTTP 注解管理，不表示该 API 已上线或已禁用。手动关联后，API 的路径、方法和插件仍通过原 API 编辑页配置、按原流程发布。已绑定 API 不允许直接移除 Kitex 转换或改成其他上游、Service、Method。

全量更新使用完整 IDL 方法列表判断绑定是否仍然存在，与“解析无 HTTP 注解的方法”展示选项无关：

- 手动方法仍存在：仅更新契约引用和入口文件，保留手动路由、插件和启停设置。后来增加 HTTP 注解也不会接管手动 API。
- 自动方法仅移除 HTTP 注解：生成禁用草稿。此时可在契约列表点击“配置”明确转为手动管理，保留原 API UUID。
- 方法或整个 IDL 被删除：所有关联 API 生成禁用草稿，发布后生效，不立即删除历史配置。
- 恢复历史契约：同时恢复 API 配置和自动/手动来源；晚于目标版本创建的 API 生成禁用草稿。

手动创建或接管会生成新的契约版本，其他 API 保留原版本快照。手动编辑 API 后，普通 API 发布使用其当前草稿；契约发布仍使用该契约版本固定的快照。

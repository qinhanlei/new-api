# 素材库

素材库统一管理 AIGC 图片、视频、音频及经真人认证的私域人像素材，默认关闭。真人库使用 `LivenessFace` 类型，认证由火山方舟 H5 完成，本站不采集或存储身份证件、认证照片或生物识别数据；后续上传的素材原件仍存储于已配置的私有 S3。

## 启用与配置

### 1. 配置 S3 存储

超级管理员打开**系统设置 → 运营设置 → 素材存储**，选择服务商并填写 S3 接口地址、区域、存储桶和密钥，测试连接后启用并保存。素材库页的**存储设置**可直达此处。测试与保存无需额外密码验证。

以火山 TOS 北京区域为例：

| 字段 | 填写内容 |
| --- | --- |
| 服务商 | 火山引擎 TOS |
| S3 接口地址 | `https://tos-s3-cn-beijing.volces.com` |
| 区域 | `cn-beijing` |
| 存储桶 | 已创建的私有 Bucket 名称 |
| Access Key / Secret Key | 对该 Bucket 有读、写、删除权限的密钥 |
| 路径式寻址 | 关闭，选择 TOS 时自动设置 |

其他区域使用对应的 [TOS S3 Endpoint](https://docs.volcengine.com/docs/torch-object-storage/regions-and-endpoints?lang=zh)。接口地址不包含桶名或路径；省略协议时自动补 `https://`，自建存储可显式填写 `http://`。

Bucket 保持私有。New API 为上游生成临时读取链接，因此存储地址必须能被上游访问。HTTP 存储可以供服务端读写，但 HTTPS 后台中的预览可能被浏览器阻止，连接测试不验证浏览器预览。

### 2. 配置渠道素材接口

先安装或更新支持 `asset-references@1` 的 doubao 任务插件，并准备仅含一个 API Key 的生成渠道。

打开**素材库 → 渠道设置**即可查看已有的**火山方舟**渠道列表，支持按名称或 ID 搜索及分页。点击目标渠道行的**编辑**，启用素材能力，填写上游提供的官方兼容素材 API 完整 HTTPS 地址及上游项目名。该列表仅显示火山方舟类型（`45`）的渠道。地址包含必要的路径，不包含 `Action`、`Version` 等查询参数。

保存成功或点击**返回列表**后，保留搜索条件、页码和列表滚动位置；保存失败保留输入。未保存时返回或关闭需确认放弃修改。

- 默认复用所选渠道的 API Key，以 **Bearer** 鉴权，无需重复填写。
- 上游素材接口要求 AK/SK 时，打开**使用 AK/SK 鉴权**，填写有素材权限的 Access Key 和 Secret Key。此配置适用于整个素材接口，包括素材管理和真人认证；火山官方及部分中转需要这种方式。已保存的 AK/SK 配置会自动展开，Secret Key 留空保留原值。

鉴权方式以上游素材接口文档为准，与视频生成接口可能不同。系统按每个渠道的配置调用兼容的 `Action`、`Version` 和 `Result` 协议，不按中转商域名判断，也不会在鉴权失败后自动更换方式重试。

图片、视频、音频的**大小上限（MB）**可分别填写正整数；留空或填写 `0` 继承[官方默认](https://docs.volcengine.com/docs/ark/create-asset-api?lang=zh)：图片小于 30 MB、视频不超过 200 MB、音频不超过 15 MB。`1 MB = 1,000,000 字节`，图片严格小于填写值，视频、音频包含上限。各家上游要求不同时，按其要求填写；系统不根据服务商名称推断限制。

本站按类型取所有已启用且素材配置有效渠道的最小值，统一用于上传和 URL 导入；停用、未配置或账号已变更的渠道不参与。该上限为全站统一规则，不随用户权限或本次生成选择的渠道变化。例如两个渠道的视频上限为 200 MB 和 50 MB，则本站视频上限为 50 MB。后台添加素材时显示当前生效上限，实际读取的文件字节数由后端校验。

配置需要渠道读取和敏感配置写入权限；保存无需额外身份验证。每家上游分别选择对应渠道配置。

保存启用的配置时，会先调用上游 `ListAssetGroups` 验证地址、凭据和项目；查询失败不保存，空分组列表可以正常保存。保存检查和后台查询统一使用 `PageNumber`、`PageSize` 页码分页。关闭素材能力无需访问上游。此检查不会创建上游项目或分组；项目需已存在，分组仍在使用素材时按需同步。

上游通过重定向要求补上地址末尾 `/` 时，系统会在原 HTTPS 地址上补齐并重发一次，保留 POST 请求体及正确签名；不会跟随跳转到其他主机、其他路径或 HTTP 地址。

若保存时提示 HTTP 404 或 405，请核对素材 API 的完整地址及路径，不能直接用站点首页或视频生成地址代替。上游返回“项目不存在”等 JSON 错误时，会保留具体原因；请在上游创建项目，或填写已有的项目名。

### 查看已保存密钥

超级管理员可在存储或渠道 AK/SK 配置的密钥输入框为空时，点击**眼睛按钮**，验证后查看已保存密钥；已输入新密钥时，眼睛按钮只切换输入内容的显隐，无需验证。按钮提示会说明当前操作。已启用 2FA 或 Passkey 时使用已注册的验证方式；未启用时使用当前密码，无密码账号使用已绑定的 OAuth 身份。被锁定或停用的验证方式不会降级到密码。只有一种验证方式时直接展示表单，多种方式时显示切换栏。查看结果在只读弹窗中展示，不回填编辑框；关闭弹窗、切换渠道、权限变化或退出登录后清除，需要再次验证才能重新查看。

读取接口为 `POST /api/option/asset_storage/secret` 和 `POST /api/channel/{id}/assets/secret`，分别要求 `asset.storage.secret.read`（空上下文）和 `channel.assets.secret.read`（上下文 `{"channel_id":id}`）的一次性 `X-Security-Proof`。个人访问令牌分别还需 `option:write`、`channel:secret_view`，账号仍须为超级管理员。成功响应仅包含 `data.secret_key`，禁止 HTTP 缓存；普通配置查询仍仅返回 `has_secret`。查看操作会记录不包含密钥的审计日志，保存和测试配置的流程不变。

该流程复用统一验证的会话、权限、短期凭证和重放保护，对照 [OWASP ASVS 5.0.0](https://asvs.owasp.org/) 的 7.5.3、8.2.1–8.2.3、8.3.1，以及 [Authentication](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html)、[Session Management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)、[MFA](https://cheatsheetseries.owasp.org/cheatsheets/Multifactor_Authentication_Cheat_Sheet.html) 和 [CSRF](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html) 指南实现。

## 在后台管理素材

在素材库选择项目、分组，首次使用先**新建分组**，再通过**添加素材**上传文件或导入 URL。项目默认是 `default`；管理员可选择具体用户管理其素材，“所有用户”视图仅用于查看。

添加素材默认填写 URL，也可在下方选择或拖拽一个文件，无需选择来源或类型。选中文件后仅上传该文件，URL 草稿保留并暂时禁用；移除文件后恢复 URL 输入。无效文件或一次拖入多个文件会提示原因，并保留已有来源；提交期间不能更换来源，失败后保留输入。

名称可留空，自动使用文件名或 URL 最后一个非空路径段，保留扩展名；URL 名称解码一次，不包含查询参数或片段。自动名称清除路径、控制字符和首尾空白，超过 64 个 Unicode 字符时截断，无法提取时使用素材 ID。手动名称优先，切换来源不会覆盖；手动名称最多 64 个 Unicode 字符，清空后恢复自动命名。后端也执行相同兜底规则。

页面按文件 MIME 和扩展名预判类型，提前拒绝明显不支持、空或确定超限的文件；最终类型由后端读取实际内容确认。无后缀 URL 和无法预判类型的文件仍可提交，URL 导入继续异步处理，不额外请求一次类型探测。识别前显示“识别中”，失败后显示“无法识别”及具体错误，只有确认属于支持的图片、视频或音频格式并通过实际字节大小检查后才保存原件、同步上游。

MP4、QuickTime、WebM、Ogg、AVI 容器按轨道或逻辑流识别音视频。元数据探测最多读取 1 MiB、遍历 4096 个元素或页，超出预算或无法可靠分类时拒绝；暂不支持未知长度 Cluster 的 WebM、含 Skeleton 流或视频的 Ogg，以及纯音频的 QuickTime、AVI。此检查不代替上游的媒体完整性、编解码和内容审核。

修改项目或素材归属后，点击**应用**再操作。

素材列表支持按名称筛选。页码浏览最多覆盖当前结果的前 20,000 条，按每页数量取完整页（每页 30 条时为 19,980 条）；总数仍显示全部匹配数量，超出时会提示通过名称筛选缩小范围。完整遍历可使用下述 API 的游标分页。

列表分别显示素材名称、素材链接和创建时间；创建时间按浏览器本地时区展示，素材链接可完整复制。图片显示可点击的缩略预览，视频和音频显示对应图标，点击打开媒体预览。图片使用私有存储原图的临时访问链接，缩小显示不会减少原图下载量；采用懒加载，自动刷新列表时保留已加载图片。加载失败时最多尝试一次更新后的链接，仍失败则显示占位图标。

桌面和移动端均可勾选素材或全选当前页，确认后批量删除，每次最多 100 项。翻页、更改每页数量、筛选、项目、素材组或归属时清空选择，同页刷新保留仍存在的选中项。“所有用户”视图和冻结中的素材组不能删除。部分成功时分别显示删除、未找到和失败数量，保留失败原因，重试只提交失败项；网络中断或结果无法核实时不报告成功，刷新列表后再核对或重试。成功删除的素材立即禁止新的引用，存储原件与上游副本仍由后台等待在途任务结束后清理。

批量删除为本站管理接口 `POST /api/assets/batch-delete`，不是 `/doubao/` 的素材 Action。请求体为 `{"ProjectName":"default","Ids":["asset-na-..."]}`，管理其他用户素材时使用 `user_id` 查询参数。请求需要 `asset:write`，跨用户操作还需管理员身份和 `user:write`；不支持 `all_users=true`。按整个请求使用现有素材写操作限流，先校验原始 ID 列表不为空且最多 100 项，再去重并逐项删除。

正常处理的批次返回 `data.Items`，每项包含 `Id`、`Status`（`deleted`、`not_found` 或 `failed`），异常项带 `Error.Code` 与 `Error.Message`。`not_found` 仅表示当前归属和项目内未找到可删除素材，不计入本次成功删除；顶层 `success: true` 仅表示已返回批次结果，不表示全部删除成功。参数、鉴权或限流失败为请求级错误；已成功的单项不因其他项失败而回滚。

文件大小遵循上述全站上限；本地还检查非空内容和媒体类型，时长、尺寸、帧率、编码和内容审核仍以上游要求为准。大小校验通过不表示已通过全部上游检查。素材首先同步到一个可用渠道，其他渠道在生成请求需要时同步。等待状态变为 `Active` 后，可复制素材 URI 用于生成请求；管理员可查看渠道副本状态及错误原因。

新增更严格的渠道或调低上限不会让已有就绪副本失效。旧素材向其他渠道新建副本时，按该渠道当前大小上限检查；超限会明确失败，保留 S3 原文件和其他渠道副本。调整限制后可通过“重新同步”重试；已创建或创建结果不明的上游资源仍继续查询、对账，不会因为限制变化重新上传。

副本同步失败后，修正渠道配置，再在**渠道副本 → 重新同步**中重试。此操作需要管理员身份、渠道写权限及相应的素材归属权限，只重新排队选中的副本，保留素材 URI、S3 原文件和其他渠道的副本。已有上游 ID 时继续查询原资源；创建结果不明时，等待至少 5 分钟并完整查询上游，找到唯一匹配资源后继续使用，确认没有匹配项才重新创建。查询失败或结果不完整时保留失败状态；审核未通过的已有素材也不会通过重新上传来跳过审核。

对应后台管理接口为 `POST /api/assets/{asset_id}/replicas/{channel_id}/retry`，无需请求体；管理其他用户的素材时通过 `user_id` 指定归属。个人访问令牌需具备 `asset:write`、`channel:write`，跨用户操作还需 `user:write`。该操作不是火山素材 Action，不改变下游 `/doubao/` 的接口规范。

## API 最小示例

下游使用本站 New API 令牌，通过 `POST /doubao/?Action=...&Version=2024-01-01` 调用。支持素材和分组各自的创建、查询、列表、更新、删除，以及下述真人认证 Action。

以下示例使用已设置的 `NEW_API_URL`（本站地址，不带末尾 `/`）和 `NEW_API_KEY`。先创建分组，记录响应中的 `Result.Id`：

```bash
curl --fail-with-body "${NEW_API_URL}/doubao/?Action=CreateAssetGroup&Version=2024-01-01" \
  -H "Authorization: Bearer ${NEW_API_KEY}" \
  -H 'Content-Type: application/json' \
  -d '{"Name":"产品素材","GroupType":"AIGC"}'
```

将 `group-na-...` 替换为该分组 ID，创建素材，再记录返回的 `Result.Id`：

```bash
curl --fail-with-body "${NEW_API_URL}/doubao/?Action=CreateAsset&Version=2024-01-01" \
  -H "Authorization: Bearer ${NEW_API_KEY}" \
  -H 'Content-Type: application/json' \
  -d '{"GroupId":"group-na-...","URL":"https://example.com/product.png"}'
```

`Name` 留空时按上述规则自动命名；`AssetType` 省略或为空时自动识别。显式传入 `Image`、`Video`、`Audio` 时仍要求内容与指定类型匹配，其他非空值会拒绝。创建 URL 素材返回 ID 仅表示已接收任务；识别期间 `AssetType` 可为空，应结合 `Status` 和 `Error` 判断处理结果。文件上传管理接口 `POST /api/assets/upload` 同样支持省略名称和类型。

将 `asset-na-...` 替换为素材 ID，查询状态：

```bash
curl --fail-with-body "${NEW_API_URL}/doubao/?Action=GetAsset&Version=2024-01-01" \
  -H "Authorization: Bearer ${NEW_API_KEY}" \
  -H 'Content-Type: application/json' \
  -d '{"Id":"asset-na-..."}'
```

等待 `Result.Status` 为 `Active`，在 `/doubao/api/v3/contents/generations/tasks` 的生成请求中使用以下图片引用，并填写渠道支持的模型及其他生成参数：

```json
{
  "type": "image_url",
  "image_url": { "url": "asset://asset-na-..." },
  "role": "reference_image"
}
```

该对象放入请求的 `content` 数组；视频、音频使用对应的 URL 字段。必须使用本站返回的素材 ID，New API 会将其转换为选中渠道的上游 ID；同一请求不能混用本站素材和直接取得的上游素材 ID。

`POST /v1/video/generations` 旧入口和 `POST /v1/videos` 也支持上述引用，放入 `metadata.content` 数组即可。旧入口保留原有请求和响应格式，也支持 JSON 字符串形式的 `metadata`。素材会在分发前校验，并优先使用全部引用素材均已就绪的合法渠道；显式指定的渠道仍受约束，不会自动切换。

非默认项目需在素材管理请求中一致填写 `ProjectName`。它是用户在本站的逻辑空间，与渠道配置的上游项目不同。`ListAssetGroups`、`ListAssets` 的 `Filter.GroupType` 按库类型填写 `AIGC` 或 `LivenessFace`，两种类型分别查询；游标分页使用 `MaxResults`、`NextToken`，不与页码参数混用。

## 真人库

### 配置与使用

1. 先完成私有 S3 和单 Key 生成渠道配置。真人认证沿用该渠道的素材鉴权方式：默认使用渠道 API Key（Bearer），上游要求 AK/SK 时打开**使用 AK/SK 鉴权**并填写密钥。上游须支持 `CreateVisualValidateSession`、`GetVisualValidateResult` 和 `GetAssetGroup` 的兼容协议；凭据需要正确项目的素材权限，生成 API Key 也必须能使用该项目的素材。`ProjectName` 在本站仍是逻辑空间，上游项目取渠道配置。
2. 将站点地址 `ServerAddress` 配置为可公开访问的 HTTP 或 HTTPS 源地址，必须包含协议，可带端口（如 `http://192.0.2.10:13000`），不含路径、查询参数、片段或账号信息。真人认证复用主程序初始化后的密钥：优先使用 `CRYPTO_SECRET`，未设置时回退至 `SESSION_SECRET`，不额外检查长度。两者均未设置时使用进程随机密钥；若需跨重启或多节点恢复认证，应在所有节点配置相同的固定随机密钥。更换有效密钥会使尚未取得上游结果的认证会话无法恢复。
3. 素材库切换至**真人库**并发起认证。可在当前设备打开链接，或复制链接、扫描二维码交给需要授权的本人。H5 链接及认证凭证有效期为 30 分钟且单次使用；此期限不是人像授权的有效期。
4. 本人完成 H5 后，返回发起认证的页面确认结果。认证弹窗每 10 秒查询一次，关闭后不会在后台自动创建本地组；可重新打开弹窗从会话列表继续。必须使用原发起登录会话或凭据，已退出登录、撤销凭据或失去权限时不能继续。
5. 服务端查询上游认证结果并核对组类型、项目后创建本地真人组。再上传同一人的素材，等待上游审核为 `Active` 后使用 `asset://asset-na-...` 引用。多个真人或人脸不一致等审核错误保留上游错误码和原因，HTTP 200 不代表素材通过审核。

HTTP 回调会明文传输 `BytedToken`，生产环境应使用 HTTPS。本站允许 HTTP 回调地址，上游是否接受仍以实际响应为准；火山返回的认证 H5 链接仍要求 HTTPS。

真人组固定绑定认证时的渠道，不会自动复制到其他渠道。一个生成请求中的真人素材必须绑定同一渠道；可同时引用该渠道已就绪的 AIGC 素材。AIGC 素材尚未同步到认证渠道时返回 `409 AssetNotReady`，不同认证渠道的真人素材混用返回 `400 AssetChannelConflict`，渠道无权限或被禁用时拒绝生成。显式选中的渠道仍须满足这些约束。

本站 H5 使用语言映射为 `zh`、`zh-Hant` 或 `en`。本人在手机完成认证无需登录本站，回调页面只提示返回发起页面，不根据浏览器传入的成功或失败码创建组。会话列表不包含认证链接或上游凭证；实际链接仅由具有写权限的原发起者读取。回调路径和查询参数不会写入本站访问日志；反向代理也应关闭该路径的完整 URL 记录。

### 真人认证 API

同样通过 `/doubao/?Action=...&Version=2024-01-01` 调用：

| Action | 请求体 | 结果 |
| --- | --- | --- |
| `CreateVisualValidateSession` | `Name`、`IdempotencyKey`，可选 `Description`、本站 `ProjectName`、`ChannelId` | `BytedToken`（本站 `validation-na-*` 引用）、`H5Link`、`CallbackURL`、`Status`、`ExpiresAt` |
| `GetVisualValidateResult` | 本站 `BytedToken`、本站 `ProjectName` | 会话状态、可恢复的链接，成功后为本站 `GroupId` |

这两个 Action 是本站托管协议：`BytedToken` 是本地引用，不是上游认证凭证；`CallbackURL` 由本站生成，拒绝调用者指定回调地址。`CreateAssetGroup` 仍只创建 AIGC，不能用它跳过真人认证。

`IdempotencyKey` 为 8–128 个不含空白的字符，同一发起者重复相同请求会返回已有会话；同键不同参数返回冲突。网络失败后重试使用相同键，只有明确开始另一场认证时才更换。认证创建结果不明时不会自动重发。创建和结果请求在共用数据库中限流：AK/SK 按 AK 共享额度，Bearer 按素材接口地址与渠道 Key 共享额度，同一凭据配置到多个渠道也不会突破限制。Bearer 接口地址的主机大小写、默认 HTTPS 端口和末尾 `/` 视为相同，其他路径保留区分。

后台使用相同 Action 的 `/api/assets/actions` 接口。创建和 `GetVisualValidateResult` 均要求 `asset:write`，代其他用户操作还要求管理员身份及 `user:write`；查询安全会话摘要使用 `GET /api/assets/validation/sessions?project_name=default`，可带 `user_id`，要求 `asset:read`（跨用户另需 `user:read`）。会话只对原发起者可见。没有可复核身份的旧式管理令牌不能发起真人认证。

服务端先持久保存已取得的上游 GroupId，再查询组信息并绑定本地 ID。已有 GroupId 时，即使 H5 凭证过期，也可以在原授权仍有效时继续绑定。若尚未取得 GroupId 就超时或丢失响应，只保留待核实状态，不按名称猜测远端组或重新认证；过期凭证会被清除，但可能存在远端资源的会话继续保护渠道账号和密钥配置。此类记录需管理员在上游人工核实，当前没有自动释放未知资源保护的接口。

### 真人组删除

真人组删除返回 HTTP `202`，响应包含本地组 ID 和状态。组保留在列表中，冻结新增素材、改名、重试和新的生成引用；已有任务和正在执行的上传、同步、清理操作完成后，后台才请求删除上游组。

- `delete_pending`：等待处理或等待在途任务释放。
- `delete_unknown`：已发出请求，结果未确认；保留原件及绑定，不允许取消以恢复使用，可重试删除。
- `delete_blocked`：上游明确拒绝且之前没有不确定结果；保留原件及错误，可重试或使用本站扩展 `CancelAssetGroupDeletion` 恢复使用。
- `deleted`：确认上游删除成功或明确返回资源不存在，随后才清理原件。通过 `GetAssetGroup` 可查询归属限定的删除结果。

`CancelAssetGroupDeletion` 请求体与删除相同：`Id`、`ProjectName`。普通代理 HTTP 404 不能证明资源已删除，不会触发销毁原件。上游限制以实际错误为准：[官方删除文档](https://docs.volcengine.com/docs/ark/delete-asset-group-api?lang=zh) 对控制台创建的真人组有授权状态限制，不能据此推断所有 API 创建的组都能立即删除。

接口没有公开可用的肖像授权到期或撤销状态字段，本站不根据认证凭证期限推断授权失效，也不承诺实时感知上游撤销。生成最终仍受上游审核和授权检查约束。

安全设计参考 [OWASP ASVS 5.0.0](https://asvs.owasp.org/) 的 8.2.1–8.2.3、8.3.1–8.3.3（服务端授权及对象归属）、3.4.5、3.5.1–3.5.3（浏览器入口）、16.2.5（敏感日志），以及 [Authentication](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html)、[Session Management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)、[CSRF](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html) 指南。认证创建与确认复用本站 Authorization 凭据认证，不接受仅靠浏览器回调或 cookie 创建资源；持久化会话只存短期加密凭证，成功或到期后清除。

## 集群与维护

- 所有节点共用数据库和同一 S3 Bucket，无需共享本地目录或配置主节点地址。请勿通过 Bucket 生命周期规则提前删除 `new-api-assets/` 下的对象。
- 自动识别需要所有后台任务节点更新到支持省略 `AssetType` 的版本后再开放新版导入入口，不能与旧后台混跑；旧后台会误判尚未识别类型的任务。回退前先停止接收自动识别任务，并等待此类在途任务处理结束。现有字段可承载识别结果，无需数据库结构迁移。
- 文件上传复用 multipart 已缓冲的数据或临时文件，URL 导入使用有大小限制的临时文件；临时文件在操作结束后关闭并删除。S3 仍是唯一持久原件，无需共享临时目录。反向代理的请求体上限也应允许本站配置的最大文件及 multipart 开销。
- 没有合法渠道同时具备全部引用素材的就绪副本时，生成请求返回 HTTP `409`、`AssetNotReady` 和 `Retry-After`，按响应提示稍后重试。
- 删除后立即禁止新的素材引用；后台等待在途任务结束，再清理上游副本和 S3 对象。
- 现有素材资源未清理完毕时，不能切换存储位置、Bucket 或相关渠道账号身份（包括生成 Key、上游项目和素材 API 主机）。仅当该渠道所有未删除副本都失败、没有已知上游 ID、没有占用中的任务且资源未被删除时，允许修正同一主机下的素材 API 路径；保存仍保留不确定的创建记录，之后通过“重新同步”核对恢复。其他变更需先删除相关资源并等待清理完成。
- 关闭功能不会迁移或删除现有素材；旧实验版素材库的表和配置不自动迁入本实现。

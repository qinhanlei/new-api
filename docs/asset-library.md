# 素材库

素材库统一管理 AIGC 图片、视频、音频及其各渠道副本，默认关闭。素材仍需通过上游审核，本功能不包含真人核验流程。

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

- **Bearer**：复用所选渠道的 API Key，无需重复填写。
- **AK/SK**：用于火山官方控制面，填写有素材权限的 Access Key 和 Secret Key。

图片、视频、音频的**大小上限（MB）**可分别填写正整数；留空或填写 `0` 继承[官方默认](https://docs.volcengine.com/docs/ark/create-asset-api?lang=zh)：图片小于 30 MB、视频不超过 200 MB、音频不超过 15 MB。`1 MB = 1,000,000 字节`，图片严格小于填写值，视频、音频包含上限。各家上游要求不同时，按其要求填写；系统不根据服务商名称推断限制。

本站按类型取所有已启用且素材配置有效渠道的最小值，统一用于上传和 URL 导入；停用、未配置或账号已变更的渠道不参与。该上限为全站统一规则，不随用户权限或本次生成选择的渠道变化。例如两个渠道的视频上限为 200 MB 和 50 MB，则本站视频上限为 50 MB。后台添加素材时显示当前生效上限，实际读取的文件字节数由后端校验。

配置需要渠道读取和敏感配置写入权限；保存无需额外身份验证。每家上游分别选择对应渠道配置。

保存启用的配置时，会先调用上游 `ListAssetGroups` 验证地址、凭据和项目；查询失败不保存，空分组列表可以正常保存。保存检查和后台查询统一使用 `PageNumber`、`PageSize` 页码分页。关闭素材能力无需访问上游。此检查不会创建上游项目或分组；项目需已存在，分组仍在使用素材时按需同步。

上游通过重定向要求补上地址末尾 `/` 时，系统会在原 HTTPS 地址上补齐并重发一次，保留 POST 请求体及正确签名；不会跟随跳转到其他主机、其他路径或 HTTP 地址。

若保存时提示 HTTP 404 或 405，请核对素材 API 的完整地址及路径，不能直接用站点首页或视频生成地址代替。上游返回“项目不存在”等 JSON 错误时，会保留具体原因；请在上游创建项目，或填写已有的项目名。

### 查看已保存密钥

超级管理员可在存储或渠道 AK/SK 配置的密钥字段旁点击**验证后查看**。已启用 2FA 或 Passkey 时使用已注册的验证方式；未启用时使用当前密码，无密码账号使用已绑定的 OAuth 身份。被锁定或停用的验证方式不会降级到密码。查看结果独立展示，不覆盖正在编辑的密钥；关闭弹窗、切换渠道或退出登录后清除，需要再次验证才能重新查看。

读取接口为 `POST /api/option/asset_storage/secret` 和 `POST /api/channel/{id}/assets/secret`，分别要求 `asset.storage.secret.read`（空上下文）和 `channel.assets.secret.read`（上下文 `{"channel_id":id}`）的一次性 `X-Security-Proof`。个人访问令牌分别还需 `option:write`、`channel:secret_view`，账号仍须为超级管理员。成功响应仅包含 `data.secret_key`，禁止 HTTP 缓存；普通配置查询仍仅返回 `has_secret`。查看操作会记录不包含密钥的审计日志，保存和测试配置的流程不变。

该流程复用统一验证的会话、权限、短期凭证和重放保护，对照 [OWASP ASVS 5.0.0](https://asvs.owasp.org/) 的 7.5.3、8.2.1–8.2.3、8.3.1，以及 [Authentication](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html)、[Session Management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)、[MFA](https://cheatsheetseries.owasp.org/cheatsheets/Multifactor_Authentication_Cheat_Sheet.html) 和 [CSRF](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html) 指南实现。

## 在后台管理素材

在素材库选择项目、分组，首次使用先**新建分组**，再通过**添加素材**上传文件或导入 URL。项目默认是 `default`；管理员可选择具体用户管理其素材，“所有用户”视图仅用于查看。

修改项目或素材归属后，点击**应用**再操作。

文件大小遵循上述全站上限；本地还检查非空内容和媒体类型，时长、尺寸、帧率、编码和内容审核仍以上游要求为准。大小校验通过不表示已通过全部上游检查。素材首先同步到一个可用渠道，其他渠道在生成请求需要时同步。等待状态变为 `Active` 后，可复制素材 URI 用于生成请求；管理员可查看渠道副本状态及错误原因。

新增更严格的渠道或调低上限不会让已有就绪副本失效。旧素材向其他渠道新建副本时，按该渠道当前大小上限检查；超限会明确失败，保留 S3 原文件和其他渠道副本。调整限制后可通过“重新同步”重试；已创建或创建结果不明的上游资源仍继续查询、对账，不会因为限制变化重新上传。

副本同步失败后，修正渠道配置，再在**渠道副本 → 重新同步**中重试。此操作需要管理员身份、渠道写权限及相应的素材归属权限，只重新排队选中的副本，保留素材 URI、S3 原文件和其他渠道的副本。已有上游 ID 时继续查询原资源；创建结果不明时，等待至少 5 分钟并完整查询上游，找到唯一匹配资源后继续使用，确认没有匹配项才重新创建。查询失败或结果不完整时保留失败状态；审核未通过的已有素材也不会通过重新上传来跳过审核。

对应后台管理接口为 `POST /api/assets/{asset_id}/replicas/{channel_id}/retry`，无需请求体；管理其他用户的素材时通过 `user_id` 指定归属。个人访问令牌需具备 `asset:write`、`channel:write`，跨用户操作还需 `user:write`。该操作不是火山素材 Action，不改变下游 `/doubao/` 的接口规范。

## API 最小示例

下游使用本站 New API 令牌，通过 `POST /doubao/?Action=...&Version=2024-01-01` 调用。支持素材和分组各自的创建、查询、列表、更新、删除，共十个 Action。

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
  -d '{"GroupId":"group-na-...","AssetType":"Image","URL":"https://example.com/product.png"}'
```

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

非默认项目需在素材管理请求中一致填写 `ProjectName`。它是用户在本站的逻辑空间，与渠道配置的上游项目不同。`ListAssetGroups`、`ListAssets` 的 `Filter.GroupType` 填写 `AIGC`；游标分页使用 `MaxResults`、`NextToken`，不与页码参数混用。

## 集群与维护

- 所有节点共用数据库和同一 S3 Bucket，无需共享本地目录或配置主节点地址。请勿通过 Bucket 生命周期规则提前删除 `new-api-assets/` 下的对象。
- 文件上传复用 multipart 已缓冲的数据或临时文件，URL 导入使用有大小限制的临时文件；临时文件在操作结束后关闭并删除。S3 仍是唯一持久原件，无需共享临时目录。反向代理的请求体上限也应允许本站配置的最大文件及 multipart 开销。
- 没有合法渠道同时具备全部引用素材的就绪副本时，生成请求返回 HTTP `409`、`AssetNotReady` 和 `Retry-After`，按响应提示稍后重试。
- 删除后立即禁止新的素材引用；后台等待在途任务结束，再清理上游副本和 S3 对象。
- 现有素材资源未清理完毕时，不能切换存储位置、Bucket 或相关渠道账号身份（包括生成 Key、上游项目和素材 API 主机）。仅当该渠道所有未删除副本都失败、没有已知上游 ID、没有占用中的任务且资源未被删除时，允许修正同一主机下的素材 API 路径；保存仍保留不确定的创建记录，之后通过“重新同步”核对恢复。其他变更需先删除相关资源并等待清理完成。
- 关闭功能不会迁移或删除现有素材；旧实验版素材库的表和配置不自动迁入本实现。

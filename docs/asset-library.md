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

在**素材库 → 渠道设置**中搜索并选择已有渠道，启用素材能力，填写上游提供的官方兼容素材 API 完整 HTTPS 地址及上游项目名。地址包含必要的路径，不包含 `Action`、`Version` 等查询参数。

- **Bearer**：复用所选渠道的 API Key，无需重复填写。
- **AK/SK**：用于火山官方控制面，填写有素材权限的 Access Key 和 Secret Key。

配置需要渠道读取和敏感配置写入权限；保存沿用渠道的安全验证。每家上游分别选择对应渠道配置。

## 在后台管理素材

在素材库选择项目、分组，首次使用先**新建分组**，再通过**添加素材**上传文件或导入 URL。项目默认是 `default`；管理员可选择具体用户管理其素材，“所有用户”视图仅用于查看。

修改项目或素材归属后，点击**应用**再操作。

图片最大 30 MiB，视频、音频最大 100 MiB，实际格式和使用条件还需符合上游要求。素材首先同步到一个可用渠道，其他渠道在生成请求需要时同步。等待状态变为 `Active` 后，可复制素材 URI 用于生成请求；管理员可查看渠道副本状态及错误原因。

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

非默认项目需在素材管理请求中一致填写 `ProjectName`。它是用户在本站的逻辑空间，与渠道配置的上游项目不同。`ListAssetGroups`、`ListAssets` 的 `Filter.GroupType` 填写 `AIGC`；游标分页使用 `MaxResults`、`NextToken`，不与页码参数混用。

## 集群与维护

- 所有节点共用数据库和同一 S3 Bucket，无需共享本地目录或配置主节点地址。请勿通过 Bucket 生命周期规则提前删除 `new-api-assets/` 下的对象。
- 没有合法渠道同时具备全部引用素材的就绪副本时，生成请求返回 HTTP `409`、`AssetNotReady` 和 `Retry-After`，按响应提示稍后重试。
- 删除后立即禁止新的素材引用；后台等待在途任务结束，再清理上游副本和 S3 对象。
- 现有素材资源未清理完毕时，不能切换存储位置、Bucket 或相关渠道账号身份（包括生成 Key、素材 API 地址和上游项目）。先删除资源并等待清理完成。
- 关闭功能不会迁移或删除现有素材；旧实验版素材库的表和配置不自动迁入本实现。

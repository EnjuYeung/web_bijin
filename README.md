# 美人

家里 Unraid 上用的本地图片瀑布流相册。打开先登录，再看图。点一张图看大图，再点大图回到刚才的位置。

## 启动

1. 复制配置：`cp docker-compose.example.yaml docker-compose.yml`，再执行 `cp .env.example .env`
2. 改 `PHOTOS_DIR` 为你的照片目录（会递归扫描子文件夹）
3. 改 `AUTH_USER` 和 `AUTH_PASS` 为你的登录账号
4. 需要的话改 `HOST_PORT`（容器内固定是 5001，改的是电脑/Unraid 上的端口）
5. 在本目录执行：

```bash
docker compose up -d
```

浏览器打开 `http://服务器IP:HOST_PORT`，先登录。没登录进不了首页，也看不到照片。

### 使用对象存储

支持在现有本地图库基础上接入 S3 兼容对象存储（MinIO、Cloudflare R2 及提供 S3 API 的服务）。前端操作和图片地址不变，只需在 `.env` 中启用聚合图片源：

```dotenv
STORAGE_BACKEND=s3
S3_ENDPOINT=https://s3.example.com
S3_REGION=us-east-1
S3_BUCKET=my-photos
S3_PREFIX=gallery
S3_ACCESS_KEY=replace-me
S3_SECRET_KEY=replace-me
S3_USE_SSL=true
```

`S3_PREFIX` 可留空。endpoint 已包含 `http://` 或 `https://` 时，以其中的协议为准；没有协议时由 `S3_USE_SSL` 决定。`STORAGE_BACKEND=s3` 表示同时扫描本地 `PHOTOS_DIR` 和 S3 bucket/prefix，两边图片合并进同一照片与相册列表；SQLite、会话密钥和缩略图仍保存在 `DATA_DIR`。任一图片源访问失败时会保留已有索引，不会把图库误判为空。

## 浏览照片和相册

- 左侧导航的「照片」是全部照片瀑布流；「相册」按图片直接所在文件夹展示封面、文件夹名和照片数量。
- 点击相册封面后，只浏览该文件夹里的照片。瀑布流、大图、前后翻页和返回位置与全部照片一致。
- 根目录内的图片归在「根目录」相册；嵌套文件夹按图片直接所在的那一层分别成册。
- 侧栏在电脑上默认展开、手机上默认收起。点顶部按钮可切换，选择会记在当前浏览器里。
- 右上角图形按钮可在白天、自动、黑夜之间切换。自动按 `TZ` 的当前时间判断（6:00–18:00 白天）。
- 照片顺序每次打开会打乱，同一轮滚动里保持稳定。电脑上悬停照片时，卡片位置不动，图片轻微放大并显示元数据；相册封面使用同样的轻微放大动效。
- 点开大图后，右上角的信息图标可展开完整信息；手机上默认收起。

## 环境变量

写在 `.env` 里即可。

| 变量 | 默认 | 含义 |
|---|---|---|
| `HOST_PORT` | `5001` | 宿主机端口。容器内永远是 5001 |
| `PHOTOS_DIR` | `./photos` | 宿主机上的照片目录 |
| `DATA_DIR` | `./data` | 索引和缩略图 |
| `STORAGE_BACKEND` | `local` | `local` 只看本地；`s3` 同时展示本地与 S3 |
| `S3_ENDPOINT` | （对象存储必填） | S3 兼容 endpoint，可含 `http://` 或 `https://` |
| `S3_REGION` | `us-east-1` | S3 region |
| `S3_BUCKET` | （对象存储必填） | bucket 名称 |
| `S3_PREFIX` | 空 | 可选的对象 key 前缀 |
| `S3_ACCESS_KEY` | （对象存储必填） | S3 access key，只写入 `.env` |
| `S3_SECRET_KEY` | （对象存储必填） | S3 secret key，只写入 `.env` |
| `S3_USE_SSL` | `true` | endpoint 不含协议时是否使用 HTTPS |
| `TZ` | `Asia/Shanghai` | 日志时间和日期显示 |
| `SCAN_EVERY` | `2m` | 自动再扫间隔，最短 10 秒 |
| `AUTH_USER` | `juen` | 登录用户名，必须有 |
| `AUTH_PASS` | `changeme` | 登录密码，必须有 |

加了新照片，等待自动扫描后刷新网页查看；也可以重启触发扫描，再刷新网页：

```bash
docker compose restart
```

## 支持的图片

`.jpg` `.jpeg` `.png` `.webp` `.gif`（大小写都行）。子文件夹会进去。名字以 `.` 开头的文件或文件夹会跳过。不支持 HEIC / RAW。

## 看日志

```bash
docker compose logs -f
```

## 停止

```bash
docker compose down
```

数据和缩略图在 `DATA_DIR`。本地照片保持只读挂载，S3 bucket 也只读访问，不会修改或上传任何原图。

# 美人

部署在 netcup 上的图片瀑布流相册，照片放在同机的 RustFS 对象存储里，网址 `https://csb.jgbman.cc`。打开先登录，再看图。点一张图看大图，再点大图回到刚才的位置。

## 启动

在 netcup 的 `/opt/1panel/apps/web_bijin` 里：

1. 复制配置：`cp docker-compose.example.yaml docker-compose.yml`，再执行 `cp .env.example .env`
2. 改 `AUTH_USER` 和 `AUTH_PASS` 为你的登录账号；照片全在对象存储时，`PHOTOS_DIR` 指向一个空目录即可
3. 开启上传通知时填 `EVENTS_LISTEN=:5002` 和 `EVENTS_TOKEN`（与 RustFS 端相同，见下文）
4. 在本目录执行：

```bash
docker compose up -d --build
```

容器加入 1Panel 的 `1panel-network`，只把 5001 映射到本机 `127.0.0.1`。在 1Panel「网站」里新建反向代理网站 `csb.jgbman.cc`，代理地址填 `http://127.0.0.1:5001`，再开启 HTTPS。没登录进不了首页，也看不到照片。

### 使用对象存储

对象存储在网页里配置，不写在 `.env` 里。登录后点左侧「设置」→「添加对象存储」，填好后先点「测试连接」，再点「保存」。可以添加多个，保存后马上开始扫描，不用重启容器。

- 支持兼容 Amazon S3 接口的服务，例如 AWS S3、Cloudflare R2、MinIO、Backblaze B2、Wasabi、阿里云 OSS、腾讯云 COS。
- 必填：Endpoint（例如 `https://s3.example.com`；只填协议、域名和端口，不写协议时默认 https）、Bucket、Access Key、Secret Key。前缀可留空。
- 「高级设置」一般不用动：Region 留空会自动识别；阿里云 OSS、腾讯云 COS 等只支持 `bucket.域名` 访问的服务，把寻址方式改为「虚拟主机」；很老的服务不支持新版列举接口时，勾选「旧版列举接口」。
- 存储里的照片和本地照片合并展示，同名文件夹合并为同一个相册；前缀本身不算文件夹。
- Secret Key 只保存在服务器的数据库里，网页之后不会再显示；编辑时留空表示不修改。
- 某个存储暂时连不上时，它已有的照片会保留，设置页显示原因，其他存储照常更新。删除存储只会把它的照片移出图库，不会删除存储里的原图。
- 「大图由浏览器直接从存储读取」：勾选后，看大图时浏览器拿到一个 24 小时有效的签名地址，直接从存储下载原图，不经过本服务。「浏览器访问地址」填浏览器能打开的存储地址；Endpoint 填的是服务器内网地址时必须填写。建议给相册单独建一个只读用户，签名地址里能看到它的 Access Key（看不到 Secret）。

netcup 上的现行设置：Endpoint `http://1Panel-rustfs-dVOR:9000`（容器内网）、Bucket `jan`、只读用户 `bijin-ro`、寻址方式「路径」、大图直连开启、浏览器访问地址 `https://s3.jgbman.cc`。

### 上传通知（RustFS）

开启后，往桶里上传或删除照片，通常 0–5 秒就进出图库，不用等定时扫描。RustFS 端需要（已在 netcup 配好）：

1. RustFS 的 compose 环境变量：`RUSTFS_NOTIFY_ENABLE=true`、`RUSTFS_NOTIFY_WEBHOOK_ENABLE_BIJIN=on`、`RUSTFS_NOTIFY_WEBHOOK_ENDPOINT_BIJIN=http://bijin:5002/s3-events`、`RUSTFS_NOTIFY_WEBHOOK_AUTH_TOKEN_BIJIN=<与 EVENTS_TOKEN 相同>`、`RUSTFS_NOTIFY_WEBHOOK_QUEUE_DIR_BIJIN=/notify/bijin`（挂载 `./notify:/notify`），以及放行容器内网的 `RUSTFS_OUTBOUND_ALLOW_ORIGINS=http://bijin:5002`，然后重启 RustFS。
2. 给桶绑定事件：`rc bucket event add <别名>/jan arn:rustfs:sqs:us-east-1:bijin:webhook --event "s3:ObjectCreated:*" --event "s3:ObjectRemoved:*"`。

设置页的对象存储卡片会显示「上传通知已开启」和最近收到的时间。通知漏掉时，每 30 分钟的自动核对会补上。

从旧版本升级：旧版写在 `.env` 里的 `STORAGE_BACKEND`、`S3_*` 不再生效，可以删掉；升级后在设置页重新添加这个存储即可。

## 浏览照片和相册

- 左侧导航的「照片」是全部照片瀑布流；「相册」按图片直接所在文件夹展示封面、文件夹名和照片数量。
- 点击相册封面后，只浏览该文件夹里的照片。瀑布流、大图、前后翻页和返回位置与全部照片一致。
- 根目录内的图片归在「根目录」相册；嵌套文件夹按图片直接所在的那一层分别成册。
- 侧栏在电脑上默认展开、手机上默认收起。点击侧栏网站标题 `Juen's`（收起后为 `J`）切换，选择会记在当前浏览器里；也支持键盘 Enter / 空格。
- 右上角图形按钮可在白天、自动、黑夜之间切换。自动按 `TZ` 的当前时间判断（6:00–18:00 白天）。
- 照片顺序每次打开会打乱，同一轮滚动里保持稳定。电脑上悬停照片时，卡片位置不动，图片轻微放大并显示元数据；相册封面使用同样的轻微放大动效。
- 点开大图后，右上角的信息图标可展开完整信息；手机上默认收起。
- 左侧「设置」显示本地照片目录、照片数量和最近扫描结果，并管理对象存储。本地照片目录只能在 `.env` 里改，设置页只做说明。

## 环境变量

写在 `.env` 里即可。

| 变量 | 默认 | 含义 |
|---|---|---|
| `HOST_PORT` | `5001` | 宿主机端口，只绑定 `127.0.0.1`。容器内永远是 5001 |
| `PHOTOS_DIR` | `./photos` | 宿主机上的照片目录 |
| `DATA_DIR` | `./data` | 索引、缩略图和对象存储配置 |
| `TZ` | `Asia/Shanghai` | 日志时间和日期显示 |
| `AUTH_USER` | `juen` | 登录用户名，必须有 |
| `AUTH_PASS` | `changeme` | 登录密码，必须有 |
| `THUMB_WORKERS` | CPU 数的约三分之二 | 同时生成缩略图的张数；netcup 用 4 |
| `EVENTS_LISTEN` | 空 | 上传通知端口，例如 `:5002`；留空则关闭 |
| `EVENTS_TOKEN` | 空 | 上传通知令牌，至少 16 位，与 RustFS 端相同 |
| `SCAN_EVERY` | `2m`，开通知后 `30m` | 自动再扫间隔，最短 10 秒 |
| `CPUS` / `MEM_LIMIT` / `GOMEMLIMIT` | `6` / `4g` / `3GiB` | 容器资源上限 |

开启上传通知后，新照片几秒内入库，刷新网页查看。没开通知时，等待自动扫描后刷新；也可以重启触发扫描，再刷新网页：

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

数据、缩略图和对象存储配置在 `DATA_DIR`（数据库只有 root 可读）。本地照片保持只读挂载，对象存储也只读访问，不会修改、上传或删除任何原图。

## 构建与开发

前端使用 Next.js、Tailwind CSS 与 shadcn/ui，并保留「月雾樱紫」配色。Docker 构建时自动安装锁定版本的前端依赖、导出静态页面并编译 Go；最终容器只有 Go 应用进程，服务器上不需要安装 Node 或 Go。修改源码后执行：

```bash
docker compose up -d --build
```

本地开发需要 Node.js 24+ 与 Go 1.25+：

```bash
cd frontend
npm ci
npm run build
cd ..
AUTH_USER=dev AUTH_PASS=dev-password go run .
```

浏览器通过 Go 服务访问页面和 API。修改前端后重新执行 `npm run build` 并重启 Go；生成的 `web/` 不提交 Git。主题颜色、字体与尺寸继续维护在根目录 `tokens.css`。

浏览器回归测试使用独立的本地照片、SQLite 和 S3 测试服务：

```bash
go run genphotos.go
go build -o output/bijin-ui-test .
cd frontend
npx playwright install chromium
npm run test:ui
```

先完成前端构建再运行 Go 测试：`go test ./...`、`go vet ./...`。测试账号仅用于回归测试，测试服务监听 `127.0.0.1:18092` / `18093`，不会连接实际对象存储。测试截图和报告在 `output/`。

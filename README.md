# 美人

部署在 netcup 上的图片瀑布流相册，照片放在同机的 RustFS 对象存储里，网址 `https://csb.jgbman.cc`。打开先登录，再看图。点一张图看大图，再点大图回到刚才的位置。

同一个服务还给其他网站提供公开的随机壁纸接口 `https://csb.jgbman.cc/v1/backgrounds/random`（原 nas-background 的功能，见下文「随机壁纸接口」）。

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

### 两步验证（可选）

开启后，登录除了用户名和密码，还要输入 1Password 等身份验证器 App 上每 30 秒换一次的 6 位动态码。只在登录时问一次，之后这台设备照样 30 天不用再输；公开的随机壁纸接口不受影响。

开启或更换：在本目录执行下面的命令。终端会显示二维码和密钥文字，用 App 扫码或粘贴，再输入一次 App 上的动态码确认；确认成功才写入 `.env` 并重启。

```bash
sh scripts/two-step.sh on
```

关闭（手机丢了也用这个）：恢复只用密码登录。

```bash
sh scripts/two-step.sh off
```

- 开启、更换和关闭都会让所有设备重新登录。
- 同一个动态码只能用一次；两台设备同时登录时，第二台等 App 换到下一个码再输。
- 同一个 IP 连续输错 5 次（密码或动态码都算）暂停 15 分钟，其他 IP 不受影响；15 分钟内没再输错就重新计数。
- 密钥只在 `.env` 的 `AUTH_TWO_STEP_SECRET` 里，不要手改；格式不对时服务不会启动，执行 `off` 即可恢复。

### 使用对象存储

对象存储在网页里配置，不写在 `.env` 里。登录后点左侧「设置」→「添加对象存储」，填好后先点「测试连接」，再点「保存」。可以添加多个，保存后马上开始扫描，不用重启容器。

- 支持兼容 Amazon S3 接口的服务，例如 AWS S3、Cloudflare R2、MinIO、Backblaze B2、Wasabi、阿里云 OSS、腾讯云 COS。
- 必填：Endpoint（例如 `https://s3.example.com`；只填协议、域名和端口，不写协议时默认 https）、Bucket、Access Key、Secret Key。前缀可留空。
- 「高级设置」一般不用动：Region 留空会自动识别；阿里云 OSS、腾讯云 COS 等只支持 `bucket.域名` 访问的服务，把寻址方式改为「虚拟主机」；很老的服务不支持新版列举接口时，勾选「旧版列举接口」。
- 存储里的照片和本地照片合并展示，同名文件夹合并为同一个相册；前缀本身不算文件夹。
- Secret Key 只保存在服务器的数据库里，网页之后不会再显示；编辑时留空表示不修改。
- 某个存储暂时连不上时，它已有的照片会保留，设置页显示原因，其他存储照常更新。删除存储只会把它的照片移出图库，不会删除存储里的原图。
- 「大图由浏览器直接从存储读取」：勾选后，看大图时浏览器拿到一个 24 小时有效的签名地址，直接从存储下载原图，不经过本服务。「浏览器访问地址」填浏览器能打开的存储地址；Endpoint 填的是服务器内网地址时必须填写。只浏览时，账号需要列举和读取权限；使用下文 GUI 上传时，同一个账号还需要目标范围的 `s3:PutObject` 权限。签名地址里能看到 Access Key ID，看不到 Secret。

netcup 上的现行设置：Endpoint `http://1Panel-rustfs-dVOR:9000`（容器内网）、Bucket `jan`、现有用户 `bijin-ro`（原有读取策略增加了 `jan/*` 的 `s3:PutObject`，没有更换 Key）、寻址方式「路径」、大图直连开启、浏览器访问地址 `https://s3.jgbman.cc`。

### 从网页上传

登录后打开左侧独立的「上传」模块：

- 选择「本地照片目录」或设置页已有的对象存储。本地对应 `.env` 的 `PHOTOS_DIR`；对象存储沿用已有的 Key、Secret、桶和前缀。
- 可选填存储内的目标目录。选图片支持单选和多选；选文件夹保留该文件夹名，只取直属图片，跳过子文件夹。多文件夹可以一起拖入，或逐个添加后一起开始。
- 支持 JPG / JPEG / PNG / WebP / GIF，每张最多 50 MB（50,000,000 字节），一批最多 10,000 张；隐藏文件、非图片和重复选择会跳过。同名目标文件始终跳过，不覆盖。
- 点「开始上传」后显示传输进度和图库处理状态。完成表示已索引、生成缩略图并完成现有壁纸流程（GIF 不生成壁纸）；在「照片」或「相册」即可查看。失败可重试，已保存图片的处理失败只重试处理。
- 开始后保存位置和目标目录会固定在本批；要换位置，先清空列表。页面保持打开直到本批完成。「停止上传」取消尚未保存的任务；已保存图片继续在后台处理。任务进度保存在内存，服务重启后已保存图片会由既有扫描恢复。

本地目录需要可写挂载，示例 Compose 已使用 `/photos:rw`。S3 上传需要现有账号拥有 `s3:PutObject`；不必更换成全桶的 `readwrite`。内网 Endpoint 对应的「浏览器访问地址」必须可从浏览器访问。

S3 使用当前凭据签发 15 分钟有效的单对象 PUT 地址，浏览器直接上传。桶 CORS 需允许相册网站来源的 GET / HEAD / PUT，并允许 `Content-Type`、`If-None-Match`、`X-Amz-Meta-Bijin-Upload` 请求头，暴露 `ETag`；netcup 的现有 `jan` 桶已配置 `https://csb.jgbman.cc`。Secret 不会传给浏览器。

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
- 「相册」页上方可按作者、模特筛选，都可以选多个：同一项里任一命中即可，作者和模特同时选时两项都要满足。打开相册再返回，筛选还在；关掉这个标签页后重新开始。
- 右侧可按名称、作者、模特、添加日期排序，旁边的箭头切换正序 / 倒序；没填作者或模特的相册排在最后。排序选择记在当前浏览器里。封面在名称下显示「作者 · 模特」。
- 侧栏在电脑上默认展开、手机上默认收起。点击侧栏网站标题 `Juen's`（收起后为 `J`）切换，选择会记在当前浏览器里；也支持键盘 Enter / 空格。
- 右上角图形按钮可在白天、自动、黑夜之间切换。自动按 `TZ` 的当前时间判断（6:00–18:00 白天）。
- 照片顺序每次打开会打乱，同一轮滚动里保持稳定。电脑上悬停照片时，卡片位置不动，图片轻微放大并显示元数据；相册封面使用同样的轻微放大动效。
- 点开大图后，右上角的信息图标可展开完整信息；手机上默认收起。
- 左侧「设置」显示本地照片目录、照片数量和最近扫描结果，并管理对象存储。本地照片目录只能在 `.env` 里改，设置页只做说明。
- 登录页的背景每次随机换一张壁纸（下文的壁纸成品，电脑横图、手机竖图），不再把原图发给没登录的人。

### 整理相册（作者与模特）

左侧「整理」按相册填写作者和模特：

- 每本相册 1 位作者、最多 10 位模特。点下拉框可以从已经填过的名字里选，也可以输入筛选；输入新名字后按回车（或点「添加「名字」」）即新增。改动立即保存，行内显示「已保存」或失败原因。
- 英文名字不分大小写，`G.su` 和 `g.su` 算同一个人；首尾和多余的空格会去掉。
- 可以按相册名、填写情况（作者和模特都没填 / 没填作者 / 没填模特）、作者（可多选）、模特（可多选）筛选，条件可以叠加；再勾选多本（或「全选当前列表」）一次设置作者和 / 或模特，留空的一项保持不变。比如选「没填作者」后全选，就是只给还没有作者的相册补上。
- 下方「作者与模特名单」默认折叠，点「展开名单」后可以改名（改成已有的名字即合并）和删除；删除只去掉名字，照片不受影响。
- 作者和模特跟着文件夹路径走：文件夹改名或移动后，这本相册会变成未填写；旧记录保留（改回原名即恢复），名单上方提示数量，可一键清理。
- 添加日期是相册里最早一张照片的时间：对象存储为上传时间，本地为文件修改时间。

## 随机壁纸接口

其他网站可以直接引用，不需要登录，每次打开随机换一张：

| 用途 | 地址 |
|---|---|
| 横屏 | `https://csb.jgbman.cc/v1/backgrounds/random?orientation=landscape` |
| 竖屏 | `https://csb.jgbman.cc/v1/backgrounds/random?orientation=portrait` |
| 不限 | `https://csb.jgbman.cc/v1/backgrounds/random` |

在网页样式里写 `background-image: url(地址)`，或者 `<img src="地址">` 即可。设置页的「随机壁纸」卡片显示已生成数量，并能一键复制这些地址。

- 壁纸从相册照片生成：横图最长 3840×2160，竖图最长 1440×2560，宽高比在 0.9–1.1 之间的方图两种都有；不放大原图，WebP 质量 80。GIF 不做壁纸。
- 照片进入相册后自动生成壁纸，通常比缩略图晚几秒；删除或替换照片后，旧壁纸几秒内不再出现，旧地址返回 404。
- 可选参数：`orientation`（landscape / portrait / square）、`profile`（desktop-3840 / mobile-1440）、`minWidth`、`minHeight`、`format=json`（返回图片信息而不是跳转）。没有符合条件的壁纸时返回 204；其他参数会被忽略。
- 接口只跳转到壁纸成品 `/media/sha256/…webp`（一年不可变缓存，允许任何网站引用），原图和相册其余部分仍然要登录。
- 壁纸文件放在 `DATA_DIR/wallpapers`，2,101 张约 400 MB。

以前用 nas-background 的网站，把 `https://bg.junziguozi.fun:17528/v1/backgrounds/random…` 换成上面的地址即可，参数不用改。

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
| `AUTH_TWO_STEP_SECRET` | 空 | 两步验证密钥，由 `scripts/two-step.sh` 写入；留空只用密码 |
| `THUMB_WORKERS` | CPU 数的约三分之二 | 同时生成缩略图的张数；netcup 用 4 |
| `EVENTS_LISTEN` | 空 | 上传通知端口，例如 `:5002`；留空则关闭 |
| `EVENTS_TOKEN` | 空 | 上传通知令牌，至少 16 位，与 RustFS 端相同 |
| `SCAN_EVERY` | `2m`，开通知后 `30m` | 自动再扫间隔，最短 10 秒 |
| `CPUS` / `MEM_LIMIT` / `GOMEMLIMIT` | `6` / `4g` / `3GiB` | 容器资源上限 |

GUI 上传保存后直接接入图库处理，不需要通知或手工扫描。通过其他工具往存储添加图片时，开启上传通知后几秒内入库，刷新网页查看；没开通知则等待自动扫描后刷新；也可以重启触发扫描，再刷新网页：

```bash
docker compose restart
```

## 支持的图片

`.jpg` `.jpeg` `.png` `.webp` `.gif`（大小写都行）。既有目录会递归扫描；GUI 选文件夹上传只取直属图片。名字以 `.` 开头的文件或文件夹会跳过。不支持 HEIC / RAW。

## 看日志

```bash
docker compose logs -f
```

## 停止

```bash
docker compose down
```

数据、缩略图、壁纸和对象存储配置在 `DATA_DIR`（数据库只有 root 可读）。本地照片为可写挂载，GUI 可以新增原图；S3 使用现有凭据上传。GUI 不覆盖或删除已有原图，删除存储配置仍只移出图库。

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
AUTH_USER=dev AUTH_PASS=dev-password go run -tags nodynamic .
```

浏览器通过 Go 服务访问页面和 API。修改前端后重新执行 `npm run build` 并重启 Go；生成的 `web/` 不提交 Git。主题颜色、字体与尺寸继续维护在根目录 `tokens.css`。

浏览器回归测试使用独立的本地照片、SQLite 和 S3 测试服务：

```bash
go run genphotos.go
go build -tags nodynamic -o output/bijin-ui-test .
cd frontend
npx playwright install chromium
npm run test:ui
```

先完成前端构建再运行 Go 测试：`go test -tags nodynamic ./...`、`go vet -tags nodynamic ./...`。`nodynamic` 让 WebP 编码固定使用内置的纯 Go 实现，与 Docker 构建一致。`go run genphotos.go` 会往 `photos/` 写测试图片，不要在生产目录（`photos/` 是线上本地照片目录）里执行，先把代码复制到别的目录再测。测试账号仅用于回归测试，测试服务监听 `127.0.0.1:18092` / `18093`，不会连接实际对象存储。测试截图和报告在 `output/`。

查询扩容测试：`go test -race -tags nodynamic ./...` 检查并发；`BIJIN_QUERY_SCALE=1 go test -tags nodynamic -run '^TestQueryScale$' -count=1 -v` 在临时 SQLite 里生成 10 万和 20 万条元数据，核对实际 HTTP 响应后测量 5 并发的冷态、热态和深分页，并记录缓存构建次数及内存。压测不生成对应容量的原图，也不连接生产桶。

照片索引启动时补充 `album_path` 和有效照片部分索引。进程内缓存有效 ID、随机顺序、相册汇总、壁纸候选及统计；首次打开、数据变化或缓存淘汰时会重新构建。顺序缓存最多 8 份/32 MiB，壁纸筛选最多 16 份/16 MiB，空闲 30 分钟的条目在后续请求中清理；基础 ID 与候选元数据另外计入进程内存。原有 seed、游标、目录相册和壁纸抽取规则继续使用。

# 代码审查报告

- 时间：2026-10-11 23:03:24 CST。
- 基线：`master` `fe7fd6a`（与 netcup 生产部署一致）。
- 范围：全部 Go 源码（约 6,500 行）、前端源码（约 2,700 行）、Docker / Compose、生产 OpenResty 站点配置；重点为逻辑错误、无效代码、冗余代码、功能分块、性能和鉴权流程。
- 方法：逐文件通读；在 netcup 隔离副本跑 `go vet`、`gofmt`、`deadcode`、`staticcheck` 和全部 Go 测试；用生产同版本镜像另起隔离实例（独立端口、测试账号、生成图片、本地 S3 fixture），以 curl 和真实 Chromium / WebKit 复现可疑问题；生产只读检查（公开接口、站点配置、访问日志计数、数据库只读副本）。
- 本次没有修改应用代码，没有改动生产配置、数据或容器。上一版报告（2026-09-28）的问题均已修复，内容见 Git 历史。

## 结论

整体架构仍适合这个项目，模块边界大体清楚，不需要重写或拆服务。生产数据干净：4,025 张照片、51 个相册、4,070 张壁纸，无孤儿行、无旧版本残留、无临时文件；近 29 小时无 WARN / ERROR；每 30 分钟一次的对账扫描约 1.2–1.5 秒完成。当前规模下没有需要处理的性能问题。

主要问题集中在**登录与写入门禁**：登录后的返回地址可以被构造成跳到外部网站（已用真实浏览器复现），未登录的登录接口不限制请求体大小，登录表单在脚本加载完成前提交会把密码放进网址。另有若干潜在缺陷、两处可删除的旧代码路径和几处分块不一致。

优先级：P1 有外部可利用的风险，建议尽快修；P2 有明确触发条件和实际影响；P3 低概率、潜在或纵深防御；P4 文案、一致性。

## 需要修复的问题

| 编号 | 优先级 | 问题 | 主要位置 | 证据 |
| --- | --- | --- | --- | --- |
| R1 | P1 | 登录返回地址可跳到外部网站（开放跳转） | `auth.go:233`、`server.go:59`、`frontend/components/login.tsx:41` | 真实浏览器复现 |
| R2 | P2 | 未登录的 `/api/login` 不限制请求体，可被大请求撑高内存 | `auth.go:243–260` | 隔离实例实测 |
| R3 | P2 | 登录表单没有 method / action，脚本未就绪时提交会把密码放进网址 | `login.tsx:47` | 真实浏览器复现 |
| R4 | P3 | 会话 Cookie 没有 `Secure`；生产 HSTS 配了但没生效 | `auth.go:177–187`；1Panel 站点配置 | 实测 + 生产配置 |
| R5 | P3 | 公开的 `/api/health` 返回扫描错误原文（内部地址、桶名、存储名） | `server.go:34–40`、`scan.go:82–90` | 生产 + 隔离实例实测 |
| R6 | P3 | 对象存储设置的写接口没有同源检查，与其他写接口不一致 | `server.go:99–103`、`settings.go:96–237` | 隔离实例实测 |
| R7 | P3 | 未开直连的 S3 原图转发有 45 秒硬上限，慢网络会被截断 | `source.go:600`、`server.go:329` | 隔离实例实测 |
| R8 | P3 | 存储暂时不可达时原图返回 404，日志不记原因 | `server.go:329–333` | 隔离实例实测 |
| R9 | P3 | 网页上传到本地目录的文件是 0600（只有 root 可读） | `upload.go:365` | 隔离实例实测 |
| R10 | P3 | 上传后处理的 90 秒期限包含排队时间，超时提示误导为“Endpoint 连接超时” | `upload.go:509–527`、`source.go:732` | 代码阅读，未复现 |
| R11 | P3 | Compose 默认账号 `juen` / `changeme`，让“缺账号不启动”失效 | `docker-compose.example.yaml:19–20` | 配置阅读；生产已用自定义密码 + 两步验证 |
| R12 | P4 | 扫描清理遇到第一个错误就中止本轮其余清理 | `scan.go:205–213` | 代码阅读 |
| R13 | P4 | 整理页“将合并”提示的大小写规则与服务器不同 | `frontend/components/organize-panel.tsx:78` | 代码阅读 |
| R14 | P4 | 设置页本地图库仍标“只读目录”，实际可上传写入 | `frontend/components/settings-panel.tsx:194` | 代码阅读 |

### R1：登录后可被带到外部网站

`safeNext()` 和登录页前端只拒绝 `//` 开头的地址。`/\evil.example`、`/<Tab>/evil.example` 能通过检查，而浏览器按网址标准会把它们解析成 `https://evil.example/`。

- 已登录时打开 `https://csb.jgbman.cc/login?next=/%5Cevil.example`：服务端直接 `302 Location: /\evil.example`，浏览器随即请求外部网站。
- 未登录时打开同一链接、正常输入账号密码登录：Chromium 与 WebKit 都落在外部网站。
- 风险：别人发一个看起来是自己域名的链接，登录（含动态码）后被带到仿冒页面，比如“登录已过期，请再输一次密码和动态码”，就能拿到密码和一个仍有效的动态码。
- 建议：服务端用 `url.Parse` 判断，要求无 scheme、无 host、以单个 `/` 开头，并拒绝 `\`、控制字符；前端用 `new URL(next, location.origin)`，origin 不同就回首页。两处共用同一规则并补测试。

### R2：未登录请求可用大请求体占用内存

`readLogin()` 的 JSON 分支没有 `MaxBytesReader`，在检查锁定和密码之前就把整个请求体读进内存；multipart 分支会把大文件暂存磁盘。生产 OpenResty 允许 50 MB 请求体。

- 实测（隔离实例）：1 个 45 MB 的未登录请求，内存峰值从 8 MiB 到 184 MiB；8 个并发到 952 MiB，结束后仍保留约 900 MiB。按每个请求约 120 MiB 推算，二三十个并发就会顶到生产的 3 GiB GOMEMLIMIT 和 4 GiB 容器上限，导致长时间 GC 或容器被杀后重启。
- 建议：`handleLogin` 开头加 `http.MaxBytesReader(w, r.Body, 4<<10)`；删除前端从未使用的 multipart 分支。

### R3：密码可能出现在网址里

登录表单只有 React 的 `onSubmit`，HTML 本身没有 `method` / `action`。页面脚本还没加载完时（网速慢、1Password 自动填充并提交）按回车，浏览器按默认方式提交：`GET /login?user=…&pass=…&code=…`。

- 实测：Chromium、WebKit 在脚本未运行时提交，地址都变成带密码的 `/login?user=review&pass=…`。
- 影响：密码与动态码进入地址栏、浏览历史（可能同步到其他设备）和 OpenResty 访问日志（`log_format main` 记录完整请求行）。生产访问日志自 10-04 起计数为 0，尚未发生。
- 建议：表单写 `method="post" action="/api/login"`，并带隐藏的 `next`；服务端已有表单登录与跳转分支（需先修 R1）。

### R4：Cookie 没有 Secure，HSTS 实际未发送

登录 Cookie 为 `HttpOnly; SameSite=Lax`，没有 `Secure`。生产站点在 server 层配置了 HSTS，但 `/` location 里有自己的 `add_header X-Cache`，nginx 不再继承上层 `add_header`，实测 `/`、`/login`、`/api/health` 都没有 `Strict-Transport-Security`。

- 影响：打开明确的 `http://` 链接或使用旧浏览器时，第一次请求会以明文带上 30 天有效的 Cookie，再被 301 到 HTTPS。现代浏览器多会自动改用 HTTPS，风险较低，但两条防线都是一行修复。
- 建议：代码在 `X-Forwarded-Proto: https` 或 TLS 时设置 `Secure`（本地 http 测试不受影响）；在 1Panel 站点的反向代理里也加 HSTS，或去掉 `X-Cache` 头。

### R5：公开健康检查带出内部信息

`/api/health` 不需要登录，经 OpenResty 公开，返回完整扫描状态。

- 生产实测：返回照片总数和最近扫描时间。
- 隔离实例实测：存储连不上时返回 `walk s3-2 (内网存储): Get "http://127.0.0.1:18099/private-bucket/?…": dial tcp …`。生产对应的是内网容器名、桶名和存储名；单张图片读取超时的错误还会带出对象路径（相册名、文件名）。
- 建议：公开接口只返回 `{"ok":true}`（前端只用其中的 `tz`，可一并保留）；扫描状态只在登录后的接口返回。

### R6：写接口门禁分散，设置页漏了同源检查

上传、整理、退出登录都在各自处理函数里调用 `sameOriginOK` / `sameOriginJSON`；对象存储的新增、修改、删除、测试连接没有调用。

- 实测：带有效 Cookie、`Origin: https://evil.example`、`Sec-Fetch-Site: cross-site` 时，测试连接 200、新增 201、删除进入处理（404）；同样的头发给 `/api/organize`、`/api/uploads` 返回 403。
- 目前真实浏览器里 `SameSite=Lax` 与跨域预检挡住了跨站请求，所以属于纵深防御缺口；但这些接口保存存储凭据、可让服务器访问任意地址，最不该缺这层。
- 建议：在路由注册处统一给所有写接口包一层“登录 + 同源（+ JSON）”门禁，删掉各处理函数里的重复检查和 `readStorageInput` 自己的 Content-Type 判断。

### R7：慢网络看非直连原图会被截断

`s3PhotoSource.open()` 给读取加了 45 秒期限，这个期限原本是给后台生成缩略图用的，但浏览器看原图（未开直连或签名失败回退时）也走同一路径。

- 实测：23 MB 原图，正常速度 0.09 秒完整返回；限速 150 KB/s 时只收到 13.4 MB 就断开（curl exit 18）。
- 生产唯一存储已开大图直连，日常浏览不受影响；属于潜在问题。
- 建议：转发给浏览器的路径只用请求自身的 context，不套 45 秒期限。

### R8 ～ R14（低优先级）

- R8：存储不可达时 `/original/{id}` 返回 404，与“照片不存在”无法区分，日志只有 `code=404`。建议仅 `isNotFound` 时 404，其他返回 502 并记录错误。
- R9：上传临时文件以 0600 创建，硬链接后正式文件仍是 0600（root:root），扫描进来的照片是 0644。宿主机非 root 的备份或共享工具读不到。生产本地目录目前为空。建议链接前 `Chmod(0644)`。
- R10：`start()` 给照片处理 90 秒，等待生成名额的时间也算在内。其他工具批量上传触发大量通知时，网页上传可能排队超时，界面显示“图库处理失败：连接超时：确认 Endpoint 能从服务器访问（context deadline exceeded）”，本地上传也会这样提示。网页自身最多 4 项并行，单独使用时不易触发。建议排队不设期限或单独计时，并把超时文案改为“处理排队超时，可重试”。
- R11：Compose 对 `AUTH_USER`、`AUTH_PASS` 提供默认值，`.env` 漏写时会用公开的 `changeme` 启动，代码里“缺账号拒绝启动”的保护失效。建议改为 `${AUTH_PASS:?请在 .env 设置 AUTH_PASS}`。
- R12：清理消失照片时，任何一张 `RemoveIfUnchanged` 报错（如一次 Stat 超时）就结束本轮，其余照片等下一轮；本轮的 `Seen` / `Failed` 也不更新。建议记数后继续。
- R13：整理页改名前的“将合并”提示用 `albums.ts` 的 `nameKey`（全部字母转小写），服务器和整理 module 只折叠英文字母。`Élise` → `élise` 会提示合并，实际变成两个人。现有名字是中、韩、英文，暂无影响。
- R14：本地图库卡片标“只读目录”，但上传页会写入这个目录，说明文字也写了可上传。

## 无效与冗余代码

| 项目 | 证据 | 建议 |
| --- | --- | --- |
| 旧整理写接口 `PUT /api/album-people`、`PUT/DELETE /api/people/{id}`、`DELETE /api/album-people/stale`（`organize.go:363–366` 及 `assign`、`rename`、`remove`、`cleanStale`、`respond` 和四个非事务包装） | 前端已不调用；生产访问日志最后一次调用是 10-10 01:01 CST（日志时区 +0200 记为 10-09 19:01），10-10 17:49 新整理 module 上线后只有 `POST /api/organize` | 删除路由与处理函数，保留 `*Tx` 规则；Go 测试和 Playwright 的准备 / 清理改走 `/api/organize`。DECISIONS 当时选择保留，需要用户确认 |
| 旧版缩略图迁移分支（`photo_processing.go:167–192`、`thumb.go:62`、`thumb.go:75`） | 生产 0 行 `source_version` 为空、0 行本地照片、0 个 `<id>.jpg` 旧缩略图 | 删除分支及对应用例（`source_test.go` 的迁移测试），同步 DECISIONS |
| 登录的 multipart 分支（`auth.go:254–255`） | 前端只发 JSON | 删除；表单分支在修 R3 后作为无脚本回退保留 |
| `handleLogin` 两次读取 `next`（`auth.go:290–294`） | `FormValue` 已含查询参数，随后又被查询参数覆盖 | 只保留一处 |
| `storageInput` 与 `s3Config` 字段完全相同（`settings.go:22–41`） | staticcheck S1016 | 直接类型转换或嵌入 |
| `pickWallpaper` 未使用（`legacy_query_test.go:109`） | staticcheck U1000 | 删除 |
| 上传准备每张图新建一个 MinIO 签名客户端（`upload.go:248`），并在锁外读取 `s.region` | `Presign` 已缓存 `presigner`（`source.go:556–560`） | 抽一个带锁、可复用的 `publicClient()` |
| 不可达分支：`album.go:22–24`、`thumb.go:175–177`、`thumb.go:106–108`（只服务测试替身）、`meta.go:38–41`、`upload-panel.tsx:87` | `path.Dir` 不返回空串；尺寸已解码为正；`sourceSet` 总有 `OpenVersion`；时区启动时已校验；组件只在有 settings 时渲染 | 顺手删除 |
| 重复实现：`settings-panel.tsx:201` 内联了 `scanInterval()`；`wallpaper.go:241–248` 两次 `Atoi` | 阅读 | 复用已有函数 |

`go vet`、`gofmt` 无问题；`deadcode` 没有报告不可达函数（旧接口仍注册在路由上，工具视为可达）。前端未发现未使用的组件、导出或 CSS 类。

## 功能分块

1. **写接口门禁**（对应 R6）：同源 / JSON 检查散落在 `auth.go`、`upload.go`、`organize*.go` 的处理函数里，设置页漏掉。建议在 `newRouter` 注册时统一包装，处理函数只管业务。
2. **照片处理 module 的边界**：`wallpapersReady`、`saveWallpapers` 是 `photoProcessor` 的方法，却放在 `wallpaper.go`，接收者名为 `s`（staticcheck ST1016）。生成名额 `gate` 放在 `thumbCache`，容量又被 `photoProcessor`、`scanner` 读取；`thumbCache` 另持有一份来源只给 `prepareVersion` 用。建议名额与读取归 `photoProcessor`，`thumbCache` 只管文件路径、保存与删除，壁纸方法移回 `photo_processing.go`。
3. **上传对扫描器的依赖**：`uploadAPI` 通过 `scanner` 取照片目录、像素上限和处理 module。建议直接注入这三项，减少耦合。
4. **前端名字身份规则**：`lib/albums.ts` 的 `nameKey` 和 `lib/album-organizer.ts` 的 `key` 语义不同（R13）。建议合并为一个名字模块：身份比较按服务器规则（只折叠英文字母），搜索可另用全量小写。

## 性能

实测：生产每 30 分钟对账约 4,000 张用时 1.2–1.5 秒（首轮 10-10 17:53:30.6 开始、31.99 结束，此后定时器从首轮结束起算，各轮在 :23:33 / :53:33 左右完成；有一轮同时处理新照片约 12 秒）；Go 全部测试 52 秒；查询缓存、扫描与壁纸逻辑在当前规模下无需改动。以下是低成本的可选优化：

- `statusWriter`（`server.go:405–413`）包住 ResponseWriter 后隐藏了 `io.ReaderFrom`，公开壁纸 `/media/…` 和本地原图无法用 sendfile。加一个 `ReadFrom` 透传即可恢复。
- 缩略图每次 `os.ReadFile` 整张读入再写出（`photo_processing.go:288`）。约 100 KB，影响小；可改为 `ServeContent` 直接读文件。
- 补壁纸时 `prepareVersion` 仍会生成一张随后丢弃的缩略图（`photo_processing.go:200`、`249–251`），只在补生成时浪费一次缩放和 JPEG 编码。
- 读取原图前的 `validate`（`photo_processing.go:197`）与 `OpenVersion` 自带的 If-Match / 版本核对重复，每张新图多一次 HEAD。属于已记录的设计选择，可保留。
- 前端：瀑布流每个滚动帧都遍历全部已加载卡片计算可见项，上传批次每次进度回调都重建整张列表快照。当前规模没问题，只有上万张长滚动或上万项批次时才值得改。

## 鉴权流程

做得好的部分：会话签名密钥绑定用户名、密码、两步验证密钥与 `session.key`；密码与动态码恒定时间比较；锁定检查在校验密码之前；动态码不能重放；退出只删本设备 Cookie 并清缓存；缩略图私有缓存；Secret Key 只写不读；`X-Real-IP` 由 OpenResty 用 `$remote_addr` 覆盖（已核对生产配置）。

需要补的：R1 开放跳转、R2 请求体上限、R3 表单回退、R4 Cookie `Secure` 与 HSTS、R5 健康检查内容、R6 统一写入门禁、R11 默认密码。另有两项可选加固：页面没有 `X-Frame-Options` / `frame-ancestors`（`SameSite=Lax` 已挡住跨站嵌入时的登录态，风险低）；登录失败与锁定不记录 IP，出现异常时无从排查。

## 验证记录与边界

- 详细命令、结果和环境见 `TEST_REPORT.md`。
- 未复现：R10（需要连续 90 秒以上占满生成名额）、R12（需要清理时恰好一次 Stat 失败）、R13（规则差异一目了然）。未在实体 iPhone / Safari 上测试。生产只做只读检查，没有在生产上发起任何写请求。
- 测试环境已全部清理：隔离容器、fixture 进程、工作目录（含带存储密钥的数据库副本）、三个测试镜像均已删除。netcup 上另有两个 10-09 创建的 Go 缓存卷 `bijin-gocache`、`bijin-gomod`，不是本次产生，未处理。

建议处理顺序：R1 → R2、R3 → R4、R5、R6（可与“统一写入门禁”一起做）→ 其余 P3 / P4；删除旧整理接口和旧缩略图迁移需要先确认，再连同测试一起改。

## 修复跟进（2026-10-11 23:44:23 CST）

上文保留为修复前的审查证据。按用户要求按顺序修复并删除旧代码；验证与部署记录见 `TEST_REPORT.md`、`CHANGELOG.md`。

| 编号 | 处理 |
| --- | --- |
| R1 | 已修复：`safeNext` 拒绝 `\` 与控制字符；登录页用 `URL` 核对 origin |
| R2 | 已修复：登录请求体上限 16 KiB，删除 multipart 分支 |
| R3 | 已修复：表单 `method="post" action="/api/login"`，账号密码只从请求体读取 |
| R4 | 已修复（代码）：HTTPS 下 Cookie 带 `Secure`，应用发送 HSTS。1Panel 反代里失效的 HSTS 配置未改动，可保留或删去 |
| R5 | 已修复：公开健康检查只给 `ok` 与时区 |
| R6 | 已修复：写接口门禁统一在路由注册处（`sameOrigin` / `protectWrite`） |
| R7 | 已修复：S3 读取不再自带 45 秒期限，后台处理仍由调用方设置期限 |
| R8 | 已修复：对象不存在 404，其他错误 502 并记日志 |
| R9 | 已修复：本地上传文件 0644 |
| R10 | 已修复：去掉上传处理 90 秒总期限，超时文案改为“读取或处理这张图片超时，可以重试” |
| R11 | 已修复：Compose 必填账号；`.env.example` 的密码留空。生产自己的 `docker-compose.yml` 不在 Git 中，未改动（生产 `.env` 已有自定义密码） |
| R12 | 已修复：清理遇错记数后继续 |
| R13 | 已修复：新增 `frontend/lib/names.ts`，整理 module、名字下拉、相册筛选共用 |
| R14 | 已修复：标签改为「可上传」 |
| 旧代码 | 已删除第一版整理写接口与包装函数、旧缩略图迁移与测试、`sourceObject.Backend`、`pickWallpaper`、`photoAlbumID` 不可达分支、上传页不可达分支；`storageInput` 直接转换为 `s3Config`；签名客户端复用；设置页复用 `scanInterval`；壁纸参数只解析一次；`wallpaper.go` 中照片处理方法的接收者名统一 |

复核后保留、未按上文删除的三处：`thumb.go` 在来源不支持按版本读取时的 `Open` 回退（多个测试替身依赖，属通用路径）、`fitEdge` 对 0 宽高的判断（防止除零）、`meta.go` 加载时区失败的回退（错误必须处理）。上文把它们归为“不可达分支”过于绝对。

未做：功能分块建议中的 2–4 项重构（生成名额与读取归属 `photoProcessor`、上传直接注入依赖、壁纸方法移文件），性能一节的可选优化，以及“可选加固”（`X-Frame-Options`、登录失败记录 IP）。它们不是缺陷，需要时单独安排。

# 验收记录

## 测试环境说明

- 时间：2026-10-05 CST；本次验收为并入随机壁纸接口（原 nas-background 的 `/v1/backgrounds/random`）与登录页背景改用壁纸。
- 生产：netcup VPS（AMD EPYC 9645，8 vCPU，15 GB，Debian 13），1Panel；容器 `bijin`（镜像 `bijin:local` 44.5 MB，Go 1.27 以 `-tags nodynamic` 构建，Alpine 3.22），限 6 核、4 GB，`THUMB_WORKERS=4`；RustFS 1.0.0，冷数据分层到 xHosts（英国）；网站 `csb.jgbman.cc` 由 1Panel 的 OpenResty 反代。
- 数据：桶 `jan` 共 2,101 张 JPEG（22.5 GiB，中位 9.8 MiB），其中 1,985 张已转存英国。
- 对照：nas-background（Unraid，Sharp 0.35.4）当前发布的壁纸：2,101 张，横 1,066 / 竖 1,015 / 方 20，WebP 合计 419,028,910 字节。
- 构建与测试环境：netcup 上的容器 `golang:1.27-alpine`（竞态检测另装 gcc / musl-dev）、`node:24-alpine`、`mcr.microsoft.com/playwright:v1.63.0-noble`；全部在独立副本 `/root/bijin-test` 中进行，不碰生产目录的 `photos/`（线上本地照片目录）和 `data/`。
- 公网验收：从用户的 Mac 经 `https://csb.jgbman.cc` 发起，不带 Cookie。
- 只读用户密钥只在服务器容器里从 root 可读文件读取，未写进源码、报告或日志。

## 测试方法说明

1. 写代码前先实测（阶段 0）：从 RustFS 内网读取 24 张真实原图（横 10、竖 10、方 4），用 Go 生成壁纸，与 nas-background 正在提供的 Sharp 成品比较尺寸、体积和 PSNR；1 路与 4 路分别测速度和峰值内存（限 6 核、4 GB）。
2. 自动化：`gofmt -l`、`go vet -tags nodynamic ./...`、`go test -tags nodynamic -count=1 ./...`、`-race -short`、壁纸 / 通知 / 扫描 / 并发相关用例 `-race -count=5`；`npm run typecheck`、`npm run build`；Playwright 7 项。
3. 部署：保留回退镜像 `bijin:pre-wallpaper`；停容器后备份 `bijin.db` 与 `session.key` 到 `/root/bijin-backup-20261005-pre-wallpaper`，再启动新镜像；升级后的首轮扫描补生成现有照片的壁纸。
4. 公网验收：脚本逐项请求随机接口、壁纸文件、Range、304、JSON、登录背景、登录门禁、已删除接口、错误参数和跨域预检；补生成结束后用 SQLite 只读统计并与 nas-background 对照。

## 测试结果说明

| 编号 | 功能、操作与预期 | 验证方式与实际证据 | 状态 |
| --- | --- | --- | --- |
| P01 | 壁纸尺寸与取整和 Sharp 一致 | 24/24 一致，例：9000×6209→3131×2160，4640×8256→1439×2560，2731×4275→1440×2254，7000×7000→2160×2160 与 1440×1440，750×750 不放大 | 通过 |
| P02 | 画质与体积 | 体积 −7%～+6%（多数 −1%～−3%）；与 Sharp 成品的 PSNR 33.6–46.8 dB，21/24 张 ≥ 40 dB；样本均为 sRGB 或未标注色彩空间 | 通过 |
| P03 | 速度与内存 | 1 路 0.28 张/秒、峰值约 0.7 GB；4 路 0.82 张/秒、峰值约 2.0 GB（含解码与缩略图）；桌面壁纸编码 3.0–3.8 秒，手机 1.2–2.0 秒；英国分层读取 22 MB/s | 通过 |
| T01 | 静态检查与 Go 回归 | gofmt 无输出；vet 无错误；`go test` 78 项 ok（50.1s）；`-race -short` ok（41.1s）；壁纸 / 通知 / 扫描 / 并发用例 `-race -count=5` ok（161.9s），无数据竞争 | 通过 |
| T02 | 壁纸生命周期（`TestWallpaperLifecycle`） | 横 / 竖 / 方图生成对应规格与尺寸，GIF 和坏图不生成；文件可解码为 WebP、尺寸与 SHA-256 与记录一致；不登录请求 random 得到 302、`no-store`、`*`；壁纸 200、`image/webp`、内容哈希等于地址、一年不可变缓存、ETag、CORP，HEAD、Range 206、304 正常；JSON 字段正确；筛选、204、8 种非法参数 400、未知参数忽略；300 次不带条件的随机三张照片各 60–140 次（方图不翻倍）；不变的照片再扫描不读原图；替换后照片 id 不变、新地址可用、旧地址 404、旧文件删除；删除后 random 204、地址 404、文件与记录都清掉 | 通过 |
| T03 | 补生成（`TestWallpaperBackfillForIndexedPhotos`） | 清空壁纸后再扫描：只读 2 张该有壁纸的原图（GIF 不读），照片记录与缩略图文件（内容、修改时间）不变；再扫描不再读 | 通过 |
| T04 | 壁纸失败不影响相册（`TestWallpaperFailureKeepsPhotoAndRetries`） | 壁纸目录不可写时照片照常入库、缩略图存在，扫描报告 1 个失败；恢复后下次扫描补上，失败数归零 | 通过 |
| T05 | 上传通知（`TestWallpaperFollowsStorageEvents`） | 假 S3 上传并发通知后壁纸可随机到；删除通知后 random 204、地址 404、文件清空 | 通过 |
| T06 | 登录页背景（`TestLoginBackgroundUsesWallpapers`、`TestHealthAndLoginBgPublic`） | 横屏 / 竖屏分别 302 到对应规格的壁纸、`no-store`，最终 200 `image/webp`；没有竖图时退回横图；没有壁纸时 404，不跳登录页 | 通过 |
| T07 | 只公开两个路径（`TestOnlyRandomAndMediaArePublic`） | `/v1/catalog`、`/status`、`/metrics`、`/healthz`、`/readyz` 和 5 种不合法的壁纸地址均 404；POST 405；预检 204；不登录访问照片、相册、设置、缩略图、原图均 401 | 通过 |
| T08 | 设置统计与旧库迁移 | 设置接口返回 photos 3、ready 3、横竖方各 1、字节数等于各壁纸之和；旧版数据库打开后自动有壁纸表、原照片保留 | 通过 |
| T09 | 浏览器回归（Playwright） | 原有 6 项全部通过；新增 07：卡片显示「57 / 57 张」和三条地址，复制后剪贴板内容正确，手机宽度不溢出、按钮高 ≥ 44 px；无 Cookie 的其他页面用 `<img>` 引用随机地址能显示；302 指向 `/media/sha256/…webp`，200、`image/webp`、`*`；JSON 规格正确；照片 / 设置 / 原图仍 401；登录页背景请求得到 200 的 WebP。结果 7 passed（25.0s） | 通过 |
| T10 | 构建 | `npm ci`、类型检查、静态导出成功；Docker 多阶段构建成功，镜像 44.5 MB（原 40.2 MB） | 通过 |
| D01 | 上线 | 回退镜像与数据库备份就绪；新容器启动后健康检查 healthy，监听日志正常 | 通过 |
| D02 | 补生成现有照片 | 升级后首轮扫描 00:28:39–01:09:39（41 分钟）：2,101 张、0 删除、0 失败，日志无 WARN / ERROR；生成 2,121 个壁纸（方图 20 张各两种），399 MB，无残留临时文件；数据库无过期行、无缺壁纸的照片；4 路并发 CPU 约 200–590%，`docker stats` 采样最高 2.02 GiB，cgroup 峰值（含文件缓存）2.77 GiB，均在 4 GB 上限内 | 通过 |
| D03 | 与 nas-background 对照 | 横 / 竖 / 方 1,066 / 1,015 / 20 张，`desktop-3840` / `mobile-1440` 1,086 / 1,035 个，与对方完全一致；按「路径 + 规格」逐个比对 2,121 对全部对上，其中 14 个（0.66%）一边差 1 像素（两边取整方式不同），其余尺寸相同；体积合计 413,860,868 字节，比对方少 1.2%（逐个平均 0.988 倍） | 通过 |
| A01 | 随机接口（公网，不带 Cookie） | landscape / portrait / 不带参数 / 带未知参数均 302 到 `/media/sha256/…webp`，`cache-control: no-store`，`access-control-allow-origin: *`；square 在补生成进行中返回 204，补完后 302，目标 200 `image/webp`；补完后 200 次不带条件的抽取：横 113、竖 86、方 1，190 张不同照片 | 通过 |
| A02 | 壁纸文件 | HTTP/2 200，`image/webp`，`public, max-age=31536000, immutable`，ETag 为内容哈希，`cross-origin-resource-policy: cross-origin`；下载内容 SHA-256 与地址一致；Range 206（`bytes 0-99/255208`）；If-None-Match 304 | 通过 |
| A03 | JSON | `format=json` 返回 background（照片 6336×9504、portrait、主色）、variant（mobile-1440 1440×2160）和 url | 通过 |
| A04 | 登录页背景 | `orient=land` / `port` 均 302 到壁纸、`no-store`，目标 200 `image/webp`（118 KB / 106 KB） | 通过 |
| A05 | 登录门禁 | 不登录访问 `/api/photos`、`/api/albums`、`/api/settings`、`/thumb/1`、`/original/1` 均 401；`/` 跳转 `/login` | 通过 |
| A06 | 不再提供的接口 | `/v1/catalog`、`/status`、`/metrics`、`/healthz`、`/readyz` 均 404 | 通过 |
| A07 | 参数 | `orientation=diagonal`、`minWidth=0`、`format=xml` 返回 400；`minWidth=99999` 返回 204 | 通过 |
| A08 | 跨域预检 | 以 `Origin: https://kmq.zedy.cc` 发 OPTIONS，204，允许 GET / HEAD / OPTIONS、Range / If-None-Match，`*` | 通过 |

测试期间修复：

- `gofmt` 调整了两处空行与对齐。
- Playwright 等待扫描结束的上限由默认 10 秒放宽到 60 秒：扫描现在还要编码壁纸，测试用的 58 张图需要更久，这是等待时间而不是断言的变化。
- 删除了 `TestLoginBgPrefersOrient`（测的是已删除的「按方向挑原图」函数），由 `TestLoginBackgroundUsesWallpapers` 覆盖新的登录背景；`TestHealthAndLoginBgPublic` 改为断言「没有壁纸时 404 且不跳登录页」。

## 测试结论说明

随机壁纸已并入 bijin 并在 netcup 上线：`https://csb.jgbman.cc/v1/backgrounds/random` 不登录即可使用，2,101 张照片全部有壁纸，数量与规格和 nas-background 一致；登录页背景改为壁纸；相册其余部分仍要登录。新照片的缩略图时间不变，壁纸紧随其后生成。

回退：`docker tag bijin:pre-wallpaper bijin:local`，再执行 `docker compose up -d`；数据库多出的壁纸表不影响旧版本。升级前的 `bijin.db` 与 `session.key` 备份在 `/root/bijin-backup-20261005-pre-wallpaper`。

限制与未测项：

- 没有往生产桶里上传或删除测试照片（不改动用户的桶）。上传通知触发壁纸由自动化测试覆盖（假 S3 加真实的通知处理代码）；生产上验证的是首轮扫描为 2,101 张真实照片生成壁纸。下次往桶里传照片时，设置页的「随机壁纸」数量应随之加一。
- 调用壁纸的几个网站还指向 nas-background（`bg.junziguozi.fun:17528`），尚未切换；nas-background 仍在 Unraid 运行，切换后观察一段时间再停。
- 公开接口没有应用内限流，需要时在 1Panel 网站里开流量限制。
- 画质只和 Sharp 成品对比了 sRGB 或未标注色彩空间的照片；Adobe RGB / P3 照片与缩略图一样不做色彩管理。
- 手机只用 Chromium 尺寸模拟；公网验收由脚本完成，没有人工登录浏览器（需要用户本人输入密码）。

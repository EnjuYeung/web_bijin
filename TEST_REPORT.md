# 最近一次测试记录

2026-10-09：照片处理模块与并发版本保护。

## 环境

- 开发：Linux amd64；Go 1.27.2（取自正式 `golang:1.27-alpine` 镜像）、Node 26.7.0、npm 11.19.0；Go 构建/测试加 `-tags nodynamic`，采用纯 Go WebP 编码。
- 浏览器：Playwright 1.63 / Chromium，本地单密码与两步验证两个独立服务、临时 SQLite 与真实 JPEG/PNG/GIF/WebP，独立 S3 HTTP fixture；生成并发设为 2。生产数据没有作为测试库使用。
- 生产：netcup Debian 13、AMD EPYC、Docker 29.8.1；现有 Compose、RustFS、OpenResty、数据目录和登录配置。新容器于 2026-10-09 16:44:21 CST 启动，4 个生成名额、30 分钟对账、6 核/4 GB 限额沿用。

## 方法与结果

| 编号 | 验收与操作 | 结果与证据 |
|---|---|---|
| P01 | 旧列举结果在新版本已处理后再次执行 | 先在原实现复现版本回退失败；迁入模块及校验后 `TestListedPhotoDoesNotRestoreOlderSourceVersion` 通过 |
| P02 | 用通道控制原图读取；另一调用确实进入等待后更新、删除或取消 | `TestPhotoProcessingWaitingVersionUpdateDeleteAndCancel` 通过；更新使用新版本、删除不复活、取消在持锁者释放前返回，不用 sleep 安排交错 |
| P03 | 壁纸提交前受控阻塞，真实 HTTP 列表和缩略图仍可用 | `TestPhotoProcessingThumbnailDoesNotWaitForWallpapers` 在壁纸调用仍未结束时，1 秒期限内取得可完整解码的 128×96 JPEG |
| P04 | 删除缩略图后替换源文件，按原 ID 浏览并清理旧候选 | `TestPhotoProcessingThumbnailRepairUsesCurrentVersion`：字节与返回版本/尺寸匹配，只读取一次原图，旧版本缩略图未恢复，旧清理不删新版本 |
| P05 | 同版本坏图、GIF、壁纸写入失败及恢复 | 模块结果测试及上传 HTTP 回归通过：坏图不重复读取或计新失败、上传不报完成且不可重试；GIF 不要求壁纸；壁纸失败保留可浏览照片并可重试 |
| P06 | 原图读取/缩略图写入失败、EXIF、像素限制、超时、旧库迁移和重启 | 既有回归通过；旧缩略图和相册保留，恢复后可重试；完整 45 秒 S3 正文超时实际执行 |
| P07 | 再次扫描成品齐全的未变化 S3 照片 | `TestPhotoProcessingUnchangedListingNeedsNoObjectRequests` 确认没有额外对象 HEAD/GET；既有扫描/通知/补图同图只读一次测试通过 |
| P08 | Go 全量与并发检查 | `go test -tags nodynamic -json -count=1 .`：127 通过、1 跳过，53.511 秒；`go test -race -p 2 -tags nodynamic -json -count=1 .`：127 通过、1 跳过，104.370 秒；相关并发与通知回归 `-race -count=5` 通过，12.896 秒；`go vet` 与 gofmt/diff 检查通过 |
| P09 | 前端编译、类型检查与实际浏览器操作 | `npm run build`、`npm run typecheck`、`npm run test:ui` 全通过；23 项 Chromium 测试、0 失败/跳过/flaky，87.931 秒，覆盖相册、查看、设置、人物、本地/S3 上传、失败重试、两步登录和窄屏 |
| P10 | 正式 Docker 镜像、生产本机及公网 HTTP/浏览器验证 | 见下方生产记录，全部通过 |


## 生产部署与验证

- 在 netcup 从代码提交 `89b0be8` 按原 Dockerfile 完整构建正式镜像；镜像 `sha256:331a02ad2039cd226e82804e1188cc4c5903e17b2dc2d2764ba56d1a29ae1c6c`，45,423,432 字节，revision 标签为 `89b0be8`。后续提交只补充本报告与部署记录，应用代码不再改变。
- 切换前保存停机状态的数据/配置快照并保留旧镜像；原 Compose 执行 `up -d --no-build --wait`，容器 healthy。启动扫描 1.158 秒完成：3937 seen / 3937 ready / 0 failed / 0 removed / 0 sourceErrors，`lastErr` 为空。
- 对真实 RustFS 条件读取一张 202,807 字节原图，应用 EXIF 后 750×750，与索引尺寸相同；错误版本在实际存储返回 412。该检查只读现有对象，不写测试照片。
- 本机和 `https://csb.jgbman.cc` 分别检查：未登录的照片列表/设置/缩略图/原图均 401；部署前创建的已登录会话在新进程有效，两步验证保持开启；列表 total=3937，三张缩略图与缓存文件逐字节一致，私有缓存和 304 正常；三张原图继续 302 到既有 S3 域名；公开壁纸为有效 WebP，长度及 SHA-256 与 JSON 一致，304 和 Range=206 正常。
- 公网 Chromium 实际加载桌面及 390 px 手机图库，计数 3937，至少六张缩略图成功解码；登录页动态码框、上传页与设置页可用，无横向溢出或 JavaScript 错误。截图保存在本地 `output/production-*.png`。
- SQLite integrity_check=ok；部署前后逐表排序内容哈希一致：3937 photos、3982 wallpapers、1 storage、21 people、78 album_people；`.env`、Compose、session.key 哈希一致；3937 张缩略图（163,037,046 字节）和 3982 张壁纸（692,051,464 字节）的名称与内容哈希全量一致。
- 运行二进制 `/app/bijin` 与 `/proc/1/exe` SHA-256 同为 `4f8753a58880fef2c67165a297be08f4ffc47edf6191d42c3540108c5e8f60c3`。
- 生产验收脚本曾将 HTTP 响应头当作区分大小写的字典，读取 ETag 时失败；修正脚本后，本机和公网的完整验收均重新执行并通过，浏览器脚本另一次因把显示值 `3,937` 当作未格式化的 `3937` 等待超时，改为核对已有的无歧义 aria-label 后全流程通过。两次均只修正验收脚本，没有改动应用来适配脚本。

## 结论与限制

已确认的模块与 HTTP 行为通过完整本地回归，采用具体结构体与原有 codec/storage adapter，没有新任务表、数据库迁移、队列或运行容器。源存储与 SQLite 没有跨存储事务；校验后的外部变化仍由后续通知或扫描收敛。

唯一跳过项为需显式启用的 10 万/20 万条元数据规模压测，本次未改变列表查询与缓存算法，不声称重跑该压测。生产新镜像健康，实际公网浏览与数据完整性检查通过。

原始本地证据：`/tmp/web-bijin-photo-processing-go-tests.jsonl`、`/tmp/web-bijin-photo-processing-full-race.jsonl`、`/tmp/web-bijin-photo-processing-race.log`、`/tmp/web-bijin-photo-processing-ui.log`；浏览器 JSON 和截图位于本地 `output/`（不提交生成物与测试库）。

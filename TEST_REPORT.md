# 最近一次测试记录

2026-10-09：照片处理模块与并发版本保护。

## 环境

- 开发：Linux amd64；Go 1.27.2（取自正式 `golang:1.27-alpine` 镜像）、Node 26.7.0、npm 11.19.0；Go 构建/测试加 `-tags nodynamic`，采用纯 Go WebP 编码。
- 浏览器：Playwright 1.63 / Chromium，本地单密码与两步验证两个独立服务、临时 SQLite 与真实 JPEG/PNG/GIF/WebP，独立 S3 HTTP fixture；生成并发设为 2。生产数据没有作为测试库使用。
- 生产：netcup Debian 13、AMD EPYC、Docker 29.8.1；现有 Compose、RustFS、OpenResty、数据目录和登录配置。部署验证待执行，不能视为已通过。

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
| P10 | 正式镜像构建和生产部署后验证 | 待执行 |

## 结论与限制

已确认的模块与 HTTP 行为通过完整本地回归，采用具体结构体与原有 codec/storage adapter，没有新任务表、数据库迁移、队列或运行容器。源存储与 SQLite 没有跨存储事务；校验后的外部变化仍由后续通知或扫描收敛。

唯一跳过项为需显式启用的 10 万/20 万条元数据规模压测，本次未改变列表查询与缓存算法，不声称重跑该压测。生产部署结果尚未完成，后续以实际记录替换 P10。

原始本地证据：`/tmp/web-bijin-photo-processing-go-tests.jsonl`、`/tmp/web-bijin-photo-processing-full-race.jsonl`、`/tmp/web-bijin-photo-processing-race.log`、`/tmp/web-bijin-photo-processing-ui.log`；浏览器 JSON 和截图位于本地 `output/`（不提交生成物与测试库）。

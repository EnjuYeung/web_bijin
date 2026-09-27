# 验收记录

## 测试环境说明

- 时间：2026-09-28 00:20:36 CST；基线 `master / e3cb5b96962e4bb4d54d97288004386a12f2054f`，本次为未提交的代码审查修复。
- 目标：Intel N100 / 约 15.37 GiB RAM / Unraid 7.3.2 / Linux 6.18.38-Unraid x86_64；通过 SSH 操作真实仓库。
- Go 1.23.12，`golang:1.23-alpine` 隔离容器，2 CPU / 2 GiB；race 检测在容器内安装 gcc、musl-dev。宿主机不支持 swap limit，未宣称限制了 swap。
- 源码和合成图片位于 `/tmp/bijin-fix-20260928`，测试使用临时 SQLite、受控 HTTP 服务和 1,120 张 regular 相册图片；包含其他格式和异常样本，共 1,179 个候选文件、1,178 张有效图片。
- Chromium 149.0.7827.55 / Playwright，桌面 1280×720、手机模拟 390×844。内置浏览器工具在启动 Node 运行时时失败，改用现有独立 Chromium；手机模拟不等于真实手机测试。
- 完整 Dockerfile 构建镜像 `sha256:c5856e38231d24c49725790f2ff15d9cb81274cee016a66619efe2cbddbdd940`。隔离 Compose 服务验收通过后更新生产单容器，启动于 `2026-09-28 00:19:28 CST`。

## 测试方法说明

1. 把审查结论转为正式 `review_fix_test.go` 回归用例：读取/写盘故障注入、损坏 JPEG、可控 S3 HTTP 服务、并发补图和取消、缓存条件请求、会话变更、重启/迁移及 EXIF。
2. 独立源码副本执行 `go test -count=1 -v ./...`、`go vet ./...`、静态 Linux 构建；清理旧缓存实现微调后执行 `go test -short -count=1 -v ./...`、`go vet ./...`。另执行 `go test -race -short -count=1 ./...`，默认 45 秒超时用例已在完整测试中实跑。
3. 本机执行 `node --check web/app.js`，浏览器实际提交错误/正确登录、浏览相册、刷新深处链接、取消查找、模拟第二页 503、打开关闭大图，并检查图片解码和页面异常。
4. 深处链接固定随机种子 42，实际从服务器请求分页；只固定请求 seed，不伪造照片响应。缺失目标、取消、失败场景重新加载页面，避免复用已加载的数据。
5. 从隔离源复制 Compose 与环境示例，改为独立测试名称、端口、凭据和目录；执行 `docker compose config -q`、完整 `docker build`、`docker compose up -d`，再次运行完整浏览器验收。
6. 生产更新前停止容器并备份 SQLite 与会话密钥、保留旧镜像；更新后通过真实账号登录，读取列表和相册，实际完整解码三张原图与三张缩略图，并验证 ETag 304。凭据只在测试进程内使用，没有写入报告或日志。

## 测试结果说明

| 编号 | 功能、操作与预期 | 状态与证据 |
| --- | --- | --- |
| T01 | 安装与启动：示例复制后 Compose 可解析，完整镜像能启动服务 | 通过，配置退出 0，镜像构建成功，隔离容器 healthy，完整浏览器登录通过 |
| T02 | F01/F02：v2 暂时无法读取，保留 v1 可见图及缩略图；恢复后重试成功 | 通过，`TestFixReadFailurePreservesPhotoAndRetries`，失败摘要和计数存在，恢复后版本 v2、缩略图字节变化、旧缓存清理 |
| T03 | F01：制造缩略图目标写入失败，不提交新版本，修复后可重试 | 通过，`TestFixThumbnailWriteFailureRetries` |
| T04 | F03：无效文件与文件头有效但正文截断 JPEG 排除出 ready，同版本不重读，换版本后恢复 | 通过，`TestFixCorruptImageSkippedUntilVersionChanges` 两组样本 |
| T05 | F04：同大小、同秒时间，只改来源版本，缩略图/原图 ETag 和 URL 更新；未改版本可 304 | 通过，`TestFixCacheTracksSourceVersion`，缓存策略为 `private, no-cache` |
| T06 | F05：响应头已到但正文停发，200ms 上游取消能退出；默认背景期限也能退出并释放锁 | 通过，`TestFixS3BodyCancellationAndSingleRead`、`TestFixS3DefaultDeadline`；默认用例实际运行约 45 秒 |
| T07 | F06：刷新远处大图链接，自动续页，不触发滚动或 resize | 通过，1,120 张相册、目标 id 1136，28 页后大图解码成功；`browser-final.log` |
| T08 | F06 边界：不存在的目标查到末页停止；Esc 取消；第二页 503 后停止 | 通过，分别 29 页、2 页、2 页；出现正确提示，取消后 URL hash 清除且大图关闭 |
| T09 | F07：保持 session.key，修改密码使旧 Cookie 失效；相同凭据重启仍有效 | 通过，`TestFixPasswordChangeRevokesCookie` |
| T10 | P01/P03：六个并发补图只读取一次；等待锁可取消；S3 正常处理只需一次 HEAD、一次 GET | 通过，`TestFixConcurrentMissingThumbnailAndCancelledWait`、S3 受控服务计数断言 |
| T11 | 显示正确性：EXIF 方向 6 原图尺寸 128×96，索引/缩略图为 96×128；像素超限标坏 | 通过，`TestFixOrientationAndPixelLimit`；保留已有 PNG/GIF/WebP、路径过滤和相册用例 |
| T12 | 持久化/迁移：重开数据库后不重复读取已成功版本；旧无版本本地缩略图可迁移；移除已有时间索引 | 通过，`TestFixPersistentVersionAndIndexMigration`、原有本地迁移测试、SQLite 迁移测试 |
| T13 | P04/界面：空相册明确要求扫描后刷新；大图只显示一个文件大小字段；桌面/手机模拟可点图关闭 | 通过，浏览器检查提示、图片 naturalWidth、信息栏默认状态和关闭后 hash；无 pageerror，截图已查看 |
| T14 | 全量回归与构建 | 完整 39 个顶层 Go 测试通过，`ok bijin 45.836s`；最终短回归通过 `0.748s`（仅跳过已实跑的 45 秒用例）；vet、JS 语法、Linux 构建、完整 Docker 构建及 `git diff --check` 通过 |
| T15 | 并发检查 | `go test -race -short -count=1 ./...` 通过，`ok bijin 2.755s` |
| T16 | 真实生产验证 | healthy；登录、1,584 张照片列表、14 个相册通过；三张原图完整解码为 2711×4096、3045×5120、4640×8256，对应缩略图长边均 720；六个资源的 ETag 重验证均 304 |

故障注入产生的读取错误、写入错误、截断 JPEG、像素超限日志是预期测试证据，不是被忽略的异常。隔离图库的 `broken.jpg` 在首次扫描报告失败，后续同版本扫描不再反复处理。

初次编译检查发现测试仍使用旧缩略图函数签名，以及仓库 `output` 里的历史审查取证 `.go` 被 `./...` 当成独立包编译；修正正式测试调用后在不含 `output` 的隔离源码中执行。原有扫描测试增加“坏图应上报失败”的断言，旧缓存迁移测试保留原有行为，只调整新路径检查；仅删除了无产品调用的 humanBytes 及其专用测试。没有删除失败的业务测试。

## 测试结论说明

F01–F08 已修复并有回归证据；P01–P03 已处理，P04 采用审查允许的“明确提示手动刷新”方案。完整镜像已在生产运行，真实图片抽样可用。

生产升级后的旧缩略图验证/迁移仍在后台进行：00:20:36 采样为 49 个新版本缓存，ready 保持 1,584，日志未见新的处理错误。此时不能宣称已逐张验收全部生产原图；缓存迁移和后续周期扫描由现有单进程继续完成。已缓存版本后续扫描会跳过，首次迁移期间 CPU、网络和磁盘开销高于空闲。

未执行：真实手机和长时间滚动帧率、真实 S3 厂商网络故障注入、全部生产原图逐张人工/浏览器验收、超大原图的专门内存峰值基准。同步图片解码本身不能在运算中途被 context 强行中断，超时覆盖远端 I/O、锁等待和计算结束后的检查。

证据位于 `output/review-fix-20260928/`：完整/最终短回归日志、race 日志、构建日志、浏览器脚本和日志、桌面/手机截图、生产抽样日志。旧的用户测试报告保存在其中的 `previous-test-report.md`。生产回退镜像为 `bijin:before-review-20260928`，数据库/会话备份在 `/mnt/cache/appdata/web_bijin-backups/review-fix-20260928/`。

收尾：隔离 Compose 容器和网络已删除，远端临时源码、合成图库、数据库及 Go 缓存已清理，测试证据已归档。生产容器保持 healthy，旧镜像和生产数据备份保留用于回退。未创建分支、提交或推送；AGENTS.md 未修改。

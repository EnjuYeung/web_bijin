# 最近一次测试记录

2026-10-11 23:44:23 CST：代码审查问题修复（R1–R14）与旧代码删除。基线 `fe7fd6a` 加本次改动；全部测试在 netcup 隔离环境执行，本机不运行测试。以下为提交前的验证，生产部署与验收另见下文。

## 环境

- netcup（AMD EPYC 9645、Debian 13）。工作目录 `/root/bijin-fix-20261011`，源码为本地工作区打包上传。
- 前端：`node:24-alpine`（Node 24.21.0）执行 `npm ci`、类型检查、模块测试和静态导出。
- Go：`golang:1.27-alpine`（Go 1.27.2，`-tags nodynamic`，3 核 / 3 GB，`GOMAXPROCS=3`）；race 检测加装 gcc / musl-dev。
- 浏览器：`mcr.microsoft.com/playwright:v1.63.0-noble`（Node 24.20.0、Chromium、WebKit），按仓库 `playwright.config.ts` 启动测试二进制、两步验证实例和本地 S3 fixture；`genphotos.go` 生成的 63 个文件。
- 复测实例：生产镜像 `bijin:local` 的运行环境 + 修复后的静态二进制，容器 `bijin-fix` 只监听 `127.0.0.1:5101`，测试账号、独立数据目录、2 核 / 2 GB；S3 为仓库 fixture 的副本，加一张 23,287,910 字节的噪点 JPEG，只监听本机。

## 方法与结果

| 编号 | 检查 | 方法 | 结果 |
|---|---|---|---|
| T01 | 前端类型与构建 | `npx tsc --noEmit`、`npm run build` | 通过；导出的 `login.html` 表单为 `action="/api/login" method="post"` |
| T02 | 前端模块测试 | `test:organizer`、`test:upload`、新增 `test:names` | 15 / 21 / 3 全部通过 |
| T03 | Go 静态检查 | `gofmt -l`、`go vet`、`staticcheck@master`、`deadcode` | 均无输出；审查时 staticcheck 的 3 条已消失 |
| T04 | Go 测试 | `go test -count=1 -p 2 -v .` | 149 通过、0 失败、1 跳过（`TestQueryScale` 需显式开启），53.4 秒。较审查时新增 6 个、删除 1 个旧迁移测试 |
| T05 | Go race | `go test -race -count=1 -p 2 -v .` | 149 通过、0 失败、无数据竞争，117.5 秒 |
| T06 | 浏览器回归 | `npx playwright test`（整理、上传、退出等用例的准备 / 清理改走 `/api/organize`，等待扫描改读登录后的照片列表） | 69 通过、0 失败（Chromium 43、WebKit 26），3.5 分钟 |
| V01 | 登录后跳外站（R1） | 已登录 curl `/login?next=/%5Cevil.example`、`/%09/evil.example`；Chromium / WebKit 从构造链接登录、已登录直接打开 | 修复前 `Location: /\evil.example`、浏览器落在外站；修复后均 `Location: /`，两种浏览器都停在本站相册，`next=/?view=albums` 仍正常 |
| V02 | 未登录大请求体（R2） | 未登录 POST 45 MB JSON，读 cgroup 内存 | 修复前 1 个请求 +176 MiB、8 个并发峰值 952 MiB；修复后 401 用时 0.9 毫秒，8 个并发后峰值不变（294 MiB，为实例处理测试图后的既有用量） |
| V03 | 脚本未加载时登录（R3） | 关闭 JavaScript 的 Chromium / WebKit 填表回车：错密码、正确密码 | 修复前 `GET /login?user=…&pass=…`；修复后错密码到 `/login?err=1`，正确密码登录进入 `/`，任何请求网址都不含密码 |
| V04 | Cookie 与 HSTS（R4） | 带 / 不带 `X-Forwarded-Proto: https` 登录 | https：Cookie 带 `Secure`，响应带 `Strict-Transport-Security: max-age=31536000; includeSubDomains`；http：两者都没有，本机测试照常 |
| V05 | 公开健康检查（R5） | 扫描出错后不带 Cookie 请求 `/api/health` | 修复前带 `lastErr` 等完整扫描状态；修复后只有 `{"ok":true,"tz":"Asia/Shanghai"}` |
| V06 | 设置接口同源（R6） | 带 Cookie 与外站 `Origin`、`Sec-Fetch-Site: cross-site` | 修复前测试连接 200、新增 201；修复后测试连接、新增、删除均 403「只能从相册网站修改设置」 |
| V07 | 慢网络看非直连原图（R7） | 23 MB 原图，curl 不限速与 `--limit-rate 300k` | 修复前 150 KB/s 只收到 13.4 MB 即断；修复后 300 KB/s 用时 75.8 秒完整收到，字节与源文件一致 |
| V08 | 存储不可达（R8） | 停止 fixture 后请求原图 | 修复前 404 且无日志；修复后 502，日志 `open original … connection refused` |
| V09 | 本地上传权限（R9） | API 准备 + PUT 一张本地图片 | 修复前 0600；修复后 `644 root:root`，与扫描来的照片一致 |
| V10 | Compose 必填账号（R11） | 用 `.env.example` 复制的 `.env` 执行 `docker compose config -q` | 报错「required variable AUTH_PASS is missing a value: 请在 .env 填写 AUTH_PASS」；填写后通过 |

新增 / 改写的 Go 用例：`TestSafeNext`（加 `\`、Tab、换行、NUL）、`TestSignedInLoginLinkStaysOnSite`、`TestFormLogin`（表单回退的返回地址）、`TestLoginReadsLittleAndNeverURLCredentials`、`TestHTTPSOnlyCookieAndHSTS`、`TestHealthAndLoginBgPublic`（只给 ok 与时区）、`TestSettingsWritesRefuseOtherSites`、`TestOriginalTellsMissingFromUnavailable`、`TestScanCleanupContinuesPastAPhotoItCannotCheck`（R12）、上传用例检查 0644；整理相关用例全部改走 `applyOrganize` / `POST /api/organize`，并断言第一版逐项写接口返回 404。删除 `TestLocalMigrationReusesExistingMetadataAndThumb`（对应功能已删除）。

## 未测试项及原因

- R10 上传处理排队超时：需要连续 90 秒以上占满生成名额，代价高；改动是去掉总期限并改写超时文案，由代码检查与既有上传回归覆盖。
- 实体 iPhone / Safari：没有设备，WebKit 结果不等同真机。
- 生产：提交前的验证不在生产上做任何写操作。

## 结论

审查列出的 R1–R14 已修复，其中 R1–R9、R11、R12 有修复前后的实际对比或新增用例证明；第一版整理写接口和旧缩略图迁移等旧代码已删除，原有功能的 Go、模块和双浏览器回归全部通过。测试结束后已删除 `bijin-fix` 容器、fixture 进程、隔离目录及本次拉取的 golang、node、Playwright 镜像，生产容器未改动。

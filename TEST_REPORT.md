# 最近一次测试记录

2026-10-13：原图分享（图床）功能。基线 `2c55d26` 加本次改动，全部程序与浏览器验证在 netcup 隔离环境执行；未部署生产或推送仓库。

## 环境

- netcup，Debian 13、AMD EPYC 9645。隔离源码目录 `/root/bijin-share-dev-20261013`；生产目录 `/opt/1panel/apps/web_bijin` 未改动。
- 前端 `node:24-alpine`：Node 24.21.0，2 核 / 2 GiB；仓库锁定的 Next.js、TypeScript 与依赖。
- Go `golang:1.27-alpine`：Go 1.27.2，3 核 / 3 GiB，固定 `-tags nodynamic`；竞态检测使用 gcc / musl-dev 与 `CGO_ENABLED=1`。
- 浏览器 `mcr.microsoft.com/playwright:v1.63.0-noble`：Node 24.20.0、Chromium、WebKit，3 核 / 3 GiB，单 worker。原图库为 genphotos 与既有回归用例生成的图片；S3 为仓库 fixture。
- 容器候选 `bijin-share:review-20261013`，镜像 ID `sha256:46af2e93aa280a6d092211f303f168030cfa35f9973103316667666835644113`；端口仅 `127.0.0.1:5019`，独立照片 / 数据目录、测试账号，6 核 / 4 GiB，只读根文件系统。验收容器已删除，候选镜像保留。

## 方法与结果

| 编号 | 检查及方法 | 结果 |
|---|---|---|
| T01 | `npm ci`、`npm run build`、`npm run typecheck` | 通过；Next.js 静态资源与 Go 内嵌资源实际构建完成 |
| T02 | `test:organizer`、`test:upload`、`test:names` | 15 / 21 / 3 全部通过 |
| T03 | `go test -tags nodynamic -count=1 -timeout=15m -json .` | 160 个顶层测试通过，0 失败，1 跳过；新增 11 个顶层分享测试 |
| T04 | `go test -race -tags nodynamic -count=1 -timeout=20m -json .` | 160 个顶层测试通过，0 失败，1 跳过，无数据竞争 |
| T05 | `go vet -tags nodynamic ./...`、gofmt、静态 Go 二进制构建 | 通过；最终修改的 Go 文件格式一致 |
| T06 | `npx playwright test` | 81 项通过，0 失败、0 跳过、0 flaky；Chromium 49、WebKit 32，233.1 秒 |
| T07 | 最终 HTTP 边界与删除代次调整后，`npx playwright test sharing.spec.ts` | Chromium / WebKit 各 6 项，共 12 项再次通过，26.3 秒 |
| T08 | `docker build -t bijin-share:review-20261013` | 通过，生产 `bijin:local` 标签未改动 |
| T09 | 候选镜像独立目录启动、登录、原图字节比对、已分享筛选、重启、停止 | 通过；2 张有效 JPEG，扫描 0 失败，未登录普通原图 401，分享原图字节一致，重启保留同一地址，停止后旧地址 404；日志无 WARN / ERROR |
| T10 | `git diff --check` | 通过；构建产物、测试图库 / SQLite 和日志在忽略目录，未进入代码改动 |

## 原图分享行为证据

- S01 / S09：原图默认未分享；相册列表、普通原图与分享管理继续登录保护。跨站不能开启或停止，错误 / 超长地址均 404 且不会影响有效分享。
- S02 / S11：真实 JPEG、PNG、WebP、GIF 原文件从本地来源通过分享 HTTP 返回，字节、MIME、HEAD、Range 与 ETag 正确；GIF 两帧未变。S3 JPEG 条件读取、字节比对与不跳转已验证。两个浏览器实测未登录及外站图片标签显示 JPEG 原图。分享响应 `no-store`，关闭后 Range / 条件请求同样不能绕过检查。
- S03 / S04：12 个并发复制请求得到同一链接，重启保留；停止后旧链接 404，重新开启新链接。迟到的复制 / 停止不会改变较新许可。
- S05 / S06：本地和 S3 在扫描或通知前换图，旧链接不返回新内容；本地保持相同大小与纳秒修改时间的替换、读取期间原位修改也被拒绝。读到一半停止分享，返回 404。
- S07：移动、删除、恢复文件以及复用数据库整数 id 都不恢复旧分享。删除后重新导入完全相同的文件和元数据，旧的首次复制请求仍被修改代次拒绝。临时来源故障不撤销许可，恢复后仍可用。
- S08：只看已分享的计数、随机分页、单相册范围、停止后的缓存失效已验证；浏览器切换筛选重置分页，停止后移出结果，恢复全部图片正常。
- S10：Chromium 在允许剪贴板写入的上下文中读取并比对真实剪贴板；WebKit 实际自动复制成功。拒绝剪贴板时显示可用手动地址；首次写入已成功但响应变为不完整 JSON 时核对原状态，没有复制错误地址或生成第二个分享。关闭未完成复制并重开同一张图仍可操作；390 / 320 px 手机布局可用。

第一次容器验证使用完整回归目录，其中含故意损坏的 `broken.jpg`，日志产生一条坏图 WARN 和对应扫描 ERROR；已核对是该既有负例，其他 82 张可用图片及分享链路通过。随后用两张有效 JPEG 和全新 SQLite 完成正向容器验收，扫描 0 失败且无 WARN / ERROR。没有放宽解码或隐藏该错误。

## 未测试项及范围

- `TestQueryScale` 需显式开启 `BIJIN_QUERY_SCALE=1`，本次未运行 10 万 / 20 万条规模压测；分享筛选的真实分页及原有查询回归已执行。
- 实体 iPhone / Safari 未测；WebKit 结果属于引擎验证。
- 本次未在生产账号或真实图库上执行写入，也未替换生产容器。生产 `bijin` 保持原镜像且 healthy。

## 结论与复查

按 Q1–Q14 确认的原图分享、停止分享和图库筛选已实现并在隔离环境验收。源码在本地 `master` 工作区，未提交 / 推送；候选镜像可供后续部署。

本地可检查的原始证据在忽略目录 `output/verification/`：Go JSON、浏览器日志、前端构建日志、镜像构建日志与容器验证日志；截图为 `output/share-chromium-desktop.png` 和 `output/share-chromium-mobile.png`。netcup 的相同工作目录保留测试源码、日志与候选镜像用于复查。

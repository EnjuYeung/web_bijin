# 最近一次测试报告

2026-10-22 16:39:33 CST：netcup web_bijin 图片依赖及构建工具链安全修复。基线 `2fb7fc25ad8f322240a70424badacd77559457dc`，本次只修改依赖、构建版本、回归测试及对应文档。

## 测试环境

- 全部开发、编译、扫描和浏览器测试在 netcup Debian 13 的隔离工作区 `/opt/1panel/apps/web_bijin-security-20261022` 执行，测试使用独立照片、SQLite、S3 / 识别服务 fixtures；不连接生产数据库、图库或真实 AI。
- Go 容器 `golang:1.27.3-alpine`：2 核、6 GiB、GOMAXPROCS=2、GOMEMLIMIT=3GiB、GOFLAGS=-p=1；临时目录绑定到磁盘 `/var/tmp/web-bijin-security-20261022/go-tmp`，模块与构建缓存保存在磁盘。
- 前端 `node:24.22.1-alpine`：2 核、4 GiB；浏览器 `mcr.microsoft.com/playwright:v1.63.0-noble`：2 核、4 GiB、Chromium / WebKit / Firefox。测试服务只在浏览器容器内监听 127.0.0.1:18092–18095。
- Docker 独立 builder 限 2 核、6 GiB；候选镜像 `bijin:security-20261022`，ID `sha256:b848e93da8f993cfa3e1573759bb1fd0fc7eeb657e35f201d159a6e68769db6d`。构建和测试始终使用 `-tags nodynamic`。

## 测试方法与结果

| 编号 | 实际命令 / 操作 | 结果 |
| --- | --- | --- |
| V01 | 在旧 x/image v0.30.0 下运行新增 TIFF / BMP 回归（Go 1.27.3） | 预期失败：两端序的 8 字节 TIFF 头都请求 33,554,426 字节读取；62 字节 1×1 BMP 在缩略图 JPEG 编码时越界 panic |
| V02 | 升级后以相同样本复跑 `TestTIFFHeaderReadIsBounded`、`TestMalformedPalettePhotoIsInvalidAndCanBeReplaced`，加 `TestMalformedImageUploadIsRejected` | 全部通过；另一个恶意表示为大端序 TIFF；本地上传 400 / rejected，不保存原图 |
| T01 | `npm ci`、`npm run typecheck`、`npm run build` | 通过；Next.js 16.3.8 静态导出成功 |
| T02 | `npm run test:organizer`、`test:upload`、`test:names`、`test:recognition` | 分别 15 / 21 / 3 / 6 通过，0 失败 |
| T03 | `git diff --check`、`gofmt -l *.go`、`go vet -tags nodynamic ./...` | 通过，无格式与 vet 问题 |
| T04 | `go test -tags nodynamic -count=1 -json ./...` | 182 个顶层测试通过；可选 TestQueryScale 默认跳过 1 项，0 失败 |
| T05 | `go test -race -tags nodynamic -count=1 -json ./...`（容器内安装 build-base） | 182 个顶层测试通过，同上跳过 1 项，0 处 DATA RACE |
| T06 | 新生成测试照片及 `CGO_ENABLED=0 go build -tags nodynamic -o output/bijin-ui-test .` 后执行 `npm run test:ui` | 三浏览器 129 项通过，0 失败、0 flaky、0 跳过；8.8 分钟 |
| T07 | `npm audit --package-lock-only --ignore-scripts --json` | 所有依赖共 0 项漏洞 |
| T08 | govulncheck v1.9.0：`govulncheck -tags nodynamic -json ./...` | 正常退出，已调用符号对应的漏洞公告为空 |
| T09 | `docker buildx build --builder bijin-security-20261022 --load -t bijin:security-20261022 .`；读取镜像内 /app/bijin 构建信息 | 构建通过；Go 1.27.3、x/image v0.47.0、x/text v0.43.0、CGO_ENABLED=0、nodynamic |
| T10 | 候选镜像在独立 127.0.0.1:15001 和独立照片 / SQLite 上启动 | 健康、匿名照片 401、登录后索引、缩略图、原图字节一致、随机 WebP 壁纸、内嵌静态页面全部通过 |

## 业务验收

- 合法 JPEG EXIF 方向旋转、坏图只确认一次、换版本恢复缩略图和壁纸：对应现有及新增 Go 测试通过。
- JPEG / PNG / GIF / WebP 分享、GIF 行为、本地与 S3 上传、随机壁纸生命周期：对应 Go 测试与浏览器流程通过。
- 登录、两步验证、图库浏览、整理保存、分享管理与上传恢复：三浏览器配置覆盖的 129 项全部通过。
- 独立只读补丁复核未发现可证实的存活绕过或兼容性回归；源码调用路径与实际升级后的解码器实现均已检查。详情见 CODE_REVIEW。

## 发布状态、证据与限制

- 修复镜像已构建并完成隔离验收，尚未推送或部署生产。线上仍是 `4eda428a8652`，容器启动于 2026-10-18T16:34:49Z，healthy、RestartCount=0；本次没有改生产配置、数据或容器。
- netcup 的命令日志：`/var/tmp/web-bijin-security-20261022/`；结构化 Go / race / govulncheck / Playwright 输出：隔离工作区 `output/`。升级前后关键证据另通过 Codex Security artifact 工具持久保存。
- 没有执行真实证书攻击样例；crypto/x509 修复以官方修复版、实际镜像构建信息和 govulncheck 结果确认。可选规模压测没有执行；没有 iPhone 真机或真实识别服务测试。
- 结论：授权范围内的代码修复、依赖审计、前后端回归、race、镜像构建及隔离启动验证全部通过；线上生效仍需切换修复镜像。

# 登录两步验证验收记录

## 测试环境说明

- 时间：2026-10-08 22:40–23:12 CST；用户开启后的真实登录于 23:23 CST 确认。代码 415fb48（基于 master 的 a767b02），新增登录两步验证（动态码）、按 IP 输错锁定、`bijin two-step` 子命令和 `scripts/two-step.sh`。
- netcup：Debian 13.7、Docker 29.8.1、Compose v5.5.1；Go 回归在 golang:1.27-alpine（go1.27.1 linux/amd64）容器里运行，限 6 核/4 GiB；目录 `/root/bijin-2fa-work`，与生产目录分开。
- 本地：macOS 15.8.1 x86_64、Node 26.0.0、Playwright 1.63.0、Chromium 153.0.8010.12；本机没有 Go，浏览器测试用的 darwin/amd64 二进制在 netcup 上交叉编译后取回。
- 一键脚本的端到端测试使用独立容器 `bijin-2fa-e2e`（镜像 bijin:two-step-test，127.0.0.1:18096，独立 `.env`、数据目录和测试账号），不碰生产容器。
- 测试密钥为 RFC 6238 公开样例密钥或测试中临时生成的密钥；生产密钥由用户在自己的终端里生成，没有经过对话或测试脚本。

## 测试方法说明

1. 前端 `npm ci`、`npm run typecheck`、`npm run build`；Go 端 `gofmt -l`、`go vet -tags nodynamic ./...`、`go test -tags nodynamic -count=1 ./...`，再对两步验证相关用例单独跑 `-v` 和 `-race`。
2. 终端二维码：把 `bijin two-step` 的真实终端输出逐格还原成 PNG，用 macOS CoreImage 的二维码识别器解码，核对链接和下方文字密钥一致。
3. Playwright：同一个二进制起两个服务（18092 只用密码，18094 开启两步验证），跑完整 Chromium 回归和新增 4 项。
4. 端到端：在 pty 中像人一样运行 `sh scripts/two-step.sh on/off` 并输入动态码，对隔离容器核对 `.env`、`session.key`、登录与 Cookie。
5. 生产部署后通过公网域名验证：只用密码登录、公开接口、经 OpenResty 的按 IP 锁定，以及浏览器里的登录页。
6. 用户运行 `scripts/two-step.sh on` 开启后，用手机上的动态码实际登录，并核对生产日志。

## 测试结果说明

| 编号 | 功能、操作与预期 | 方法与实际证据 | 状态 |
|---|---|---|---|
| T01 | 安装与构建 | typecheck 无错误；静态导出 4 个路由；gofmt 无输出（首次发现 config.go 对齐问题，已修正）；vet 通过；Docker 镜像 68 秒构建完成，45.4 MB | 通过 |
| T02 | 动态码算法与 RFC 6238 一致 | TestStepCodeMatchesRFC6238：官方 SHA-1 样例 6 个时间点（59 → 287082 等）全部一致 | 通过 |
| T03 | 手机时间偏差 ±30 秒可用，±60 秒拒绝；带空格可用，非 6 位数字拒绝 | TestCodeStepAllowsOneStepOfClockDrift | 通过 |
| T04 | 密钥格式：App 风格小写带空格、带填充可用；过短、非法字符拒绝；格式不对时拒绝启动 | TestParseTwoStepSecret、TestLoadConfigTwoStepSecret；端到端 E5：容器反复重启，日志 `AUTH_TWO_STEP_SECRET must be base32 text of at least 26 characters` | 通过 |
| T05 | 开启后必须输入正确动态码；同一个码只能用一次，下一个码可用 | TestTwoStepLoginNeedsFreshCode；Playwright 22、23：错码提示「用户名、密码或动态码不对」，用过的码被拒，下一个码进入相册（58 张） | 通过 |
| T06 | 同一 IP 连续输错 5 次锁 15 分钟，锁定中输对也拒绝且不延长；别的 IP 不受影响；成功或 15 分钟无失败清零；只用密码时同样锁定；表单登录跳到 `?err=locked` | TestLoginLockoutPerIP（Retry-After 900）、TestLoginFailCountResets、TestPasswordOnlyLoginIgnoresCode、TestFormLoginLockedRedirect；Playwright 24；端到端 E6 | 通过 |
| T07 | 部署（无密钥）不让人重新登录；开启、更换让旧登录失效；关闭后开启前的旧 Cookie 也不复活 | TestTwoStepSessionsSignOut；端到端 E2、E4：开启后旧 Cookie 401，关闭后开启前 Cookie 与开启期间 Cookie 都 401，session.key 每次更换 | 通过 |
| T08 | 登录页：只用密码时没有动态码框；开启时显示必填动态码框（one-time-code、数字键盘），桌面和 390 px 手机不横向溢出 | Playwright 21、22 及截图 two-step-login-desktop/mobile、two-step-locked；TestLoginOptions（`no-store`） | 通过 |
| T09 | 开启命令：二维码可被识别；输错可重试；3 次不对或没有输入则什么都不改 | TestTwoStepSetupConfirmsBeforePrinting、TestOtpauthURI、TestTerminalQRDrawsEveryModule；CoreImage 解码得到 `otpauth://totp/bijin:juen?algorithm=SHA1&digits=6&issuer=bijin&period=30&secret=…`，与文字密钥一致；端到端 E3：退出码 1、`.env` 不变、容器未重启、旧 Cookie 仍有效 | 通过 |
| T10 | 一键脚本 on/off | 端到端 9/9：先输错再输对后开启，`.env` 只多一行且等于显示的密钥，其余行和 0600 权限不变；off 删除密钥并恢复只用密码；off 能救回格式错误的密钥 | 通过 |
| T11 | Go 回归 | 121 个测试定义，常规回归 120 个执行、1 个显式压测（TestQueryScale）跳过；`ok bijin 51.855s`；两步验证相关 23 个用例逐个 PASS；相关用例 `-race` 通过（1.682 秒） | 通过 |
| T12 | 浏览器回归 | Chromium 23/23 通过（原有 19 项 + 新增 4 项），2.1 分钟 | 通过 |
| T13 | 生产部署 | 415fb48 部署 7 秒，healthy；日志 `twoStep=false`，无 WARN/ERROR；session.key 哈希部署前后都是 b973b22db1ca（已登录设备不受影响） | 通过 |
| T14 | 生产公网验证 | `/api/login-options` → `{"twoStep":false}`、`no-store`；未登录 `/api/photos`、`/original/1` → 401；只用密码登录 200，照片 3,937 张、相册 49 本；随机壁纸 JSON 200；浏览器打开登录页只有「用户名」「密码」 | 通过 |
| T15 | 生产经 OpenResty 按 IP 锁定 | 从 netcup 自身经公网域名连续输错：401×5 后 429、Retry-After 900；同时从本机输错一次仍是 401。说明 OpenResty 传入的是访客真实 IP，锁定互不影响 | 通过 |
| T16 | 生产开启两步验证后的真实登录 | 用户运行 `scripts/two-step.sh on`，23:20:07 容器重启，日志 `twoStep=true`，`.env` 有 1 行密钥（未查看内容），`/api/login-options` → `{"twoStep":true}`；用户用 1Password 动态码登录，确认成功；日志 23:23:09 `POST /api/login` 200，之前没有失败记录，随后 `/api/photos` 200；开启后无 WARN/ERROR | 通过 |

## 测试结论说明

两步验证、按 IP 输错锁定、开启/关闭命令和登录页的全部自动化测试与端到端测试通过，已部署生产。部署时没有密钥，生产仍只用密码登录，已登录的设备没有被登出。随后用户在生产开启两步验证，用「用户名 + 密码 + 动态码」第一次就登录成功，T01–T16 全部通过。

未测试 / 限制：

- 本次只跑了 Chromium，没有跑 WebKit / Safari。登录页只是多了一个普通输入框，但没有在 Safari 上实测。
- 二维码是在终端输出还原的图片上用 macOS 识别器解码的，没有用 1Password 或手机摄像头实拍终端屏幕。
- 计数只在内存：重启 bijin 会清零锁定和「用过的码」记录，重启后 90 秒内刚用过的码理论上能再用一次。
- 验证过程中有 1 次生产登录失败是测试脚本的错误：`.env` 的值带引号，脚本没去掉引号就发出请求。它只计入 netcup 自身 IP，随后的成功登录已清零；锁定探测又把 netcup 自身 IP 锁了 15 分钟，不影响其他访客。

证据：Go 测试输出、端到端 9 项结果、Playwright 列表和三张截图（`output/two-step-*.png`，本地临时文件，不入库）、生产命令输出。核心命令和结果都记录在本报告中。

# 最近一次测试记录

2026-10-10：退出登录（issue #4）。

## 环境

- 开发测试：在 netcup 上另建的副本目录 `/root/bijin-dev-logout`，不碰生产目录和数据；用 Docker 容器运行，限 3 核 / 4 GB，避免影响线上相册。Go 1.27.2（`golang:1.27-alpine`，race 另装 build-base）；前端与浏览器用 `mcr.microsoft.com/playwright:v1.63.0-noble`（Node 24.20.0、npm 11.19.0、Chromium 与 WebKit）；Docker 29.8.1。Go 构建/测试加 `-tags nodynamic`。
- 测试数据：`go run genphotos.go` 生成的 58 张本地图片、临时 SQLite、独立 S3 HTTP fixture；只用测试账号，没有用生产数据。每次完整浏览器回归前清空并重新生成，因为上传测试会往测试图片目录里加图。
- Apple WebKit 实验：本机 macOS 15.8.1（Safari 27.0），用 Swift 6.2.4 写的 WKWebView 小程序（磁盘上的独立数据目录），对一个只有 4 个路径的临时 Python 服务测试。实验程序和服务都在临时目录，不进仓库。

## 方法与结果

| 编号 | 功能 / 操作 | 预期 | 方法 | 结果与证据 |
|---|---|---|---|---|
| L01 | 退出接口：手机、电脑各登录一次，手机退出 | 303 到 `/login?out=1`，只删手机的 Cookie，`Clear-Site-Data` 只有 `"cache"`，`no-store`；电脑仍可用 | Go `TestLogoutSignsOnlyThisDeviceOut` | 通过 |
| L02 | 未登录、Cookie 已过期时退出 | 同样回登录页，不报错 | Go `TestLogoutWhenAlreadySignedOut`；浏览器 logout 06 先删 Cookie 再点按钮 | 通过 |
| L03 | 别的网站发起退出、GET 请求 | 跨站、外来 Origin、`Origin: null` 都 403 且不删 Cookie、不清缓存；GET 405；登录状态不变 | Go `TestLogoutRefusesOtherSitesAndGet`；浏览器 logout 06 从 `localhost:18092` 页面提交表单到 `127.0.0.1:18092/logout` | 通过，浏览器实际收到 403，之后接口仍 200 |
| L04 | 新测试确实依赖功能 | 去掉 `POST /logout` 路由后 3 个新 Go 测试失败 | 临时副本删掉路由后运行 | 3 个均失败（404），已删除临时副本 |
| L05 | 按钮位置 | 侧栏最下方、和五个入口隔开、在「家里的回忆」上方；收起时只剩图标且可读名称为「退出登录」；390 px 手机在屏幕内、无横向滚动 | 浏览器 logout 01（Chromium、WebKit）+ 截图 | 通过；截图 `output/logout-sidebar-desktop-*.png`、`logout-sidebar-mobile-*.png` 已人工查看 |
| L06 | 点退出 | 等待时按钮显示「正在退出…」并禁用；到登录页显示「已退出登录。」；Cookie 消失、接口 401；返回键不回相册；再登录进「照片」首页；夜间模式偏好保留 | 浏览器 logout 02（退出响应人为延迟 1.5 秒，页面把按钮状态记进 sessionStorage 供下一页读取） | 通过；登录页截图 `output/logout-login-page-*.png` 已查看 |
| L07 | 清掉缓存的缩略图 | 只删 Cookie 时缓存的缩略图仍能读到（对照）；退出后同一标签页和新标签页再取该缩略图都要回服务器，得到 401 | 浏览器 logout 03，用带磁盘缓存的真实浏览器配置 | Chromium 通过（对照 200，退出后 200→401）。Playwright 的 Linux WebKit 退出后仍从缓存给出 200，测试按实际行为断言并标注「known limitation」，见 L11 |
| L08 | 同一浏览器的其他标签页 | 一个标签页退出后，开着的另一个标签页自动跳到登录页；重新登录后新开的标签页不受影响 | 浏览器 logout 04 | 通过 |
| L09 | 上传中点退出 | 浏览器先问能否离开；选留下则不退出、按钮不卡在等待、上传继续、仍登录；选离开则退出 | 浏览器 logout 05，拦住上传正文使上传一直进行中 | 通过（两次都弹出 beforeunload 询问） |
| L10 | 浏览器对 `Clear-Site-Data: "cache"` 的实际支持 | 分别看 303 跳转、直接页面、fetch 三种响应 | Playwright 1.63 的 Chromium 与 WebKit 对临时服务做 HTTP 和 HTTPS 实验 | Chromium：三种都清掉。Linux WebKit：HTTP 和 HTTPS、三种方式都不清 |
| L11 | Apple WebKit（Safari 的内核）是否清缓存 | 同上，并在清理后用全新窗口再取一次，排除内存缓存 | 本机 WKWebView 实验，对 `http://127.0.0.1` | 三种方式都不清，新窗口也仍从缓存取。HTTPS 上的结果放到生产验证 |
| L12 | Go 全量、并发与静态检查 | 全部通过 | `go test -tags nodynamic -json -count=1 .`；`go test -race -p 2 …`；`go vet`；`gofmt -l` | 130 个顶层测试通过、1 个跳过（需显式开启的 `TestQueryScale`），51.9 秒；race 同样 130 通过 1 跳过，113.3 秒；vet 通过；gofmt 无输出 |
| L13 | 前端构建与类型检查 | 通过 | `npm ci`、`npm run build`、`npm run typecheck` | 通过 |
| L14 | 浏览器完整回归 | 原有 23 项不受影响，新增 6 项在 Chromium 与 WebKit 各跑一遍 | 重新生成测试图片后 `npx playwright test` | 35 通过、0 失败、0 跳过、0 flaky，119.5 秒；logout 02 另在两种浏览器各重复 3 次全部通过 |

过程中出现、已处理的问题：

- 第二次完整回归中原有 11 项失败：上一轮上传测试往测试图片目录加了 7 张图，导致预期的 58 张变成 65 张。重新生成测试图片后全部通过，没有改动这些测试。
- WebKit 的 logout 02 曾报一条页面错误：测试打开照片页 25 毫秒后就离开，WebKit 把被取消的照片列表请求记成错误。相册代码本身已捕获该失败，用户看不到。测试改为在没有相册代码的页面上写偏好，并等照片页加载完再结束，之后两种浏览器重复 5 次全部通过。
- 「正在退出…」的断言一开始取不到按钮：Playwright 在页面跳转等待期间不允许读取旧页面。改为让页面自己把按钮状态写进 sessionStorage，下一页再读出。

## 生产部署与验证

尚未部署，部署与公网验证完成后补充。

## 结论与限制

本地验收全部通过：只退当前设备、只清本站缓存、拒绝别的网站与 GET、其他标签页跟随、上传中先询问、返回键不回相册、偏好保留。

限制：

- 清缓存由浏览器执行。Chromium（Chrome、Edge 等）实测会清。Playwright 的 Linux WebKit 和本机 Apple WebKit（`http://127.0.0.1` 上）实测都不清，iPhone 和 Mac 上的 Safari 是否会在 HTTPS 上清，以生产验证为准。不清时退出登录仍然生效，只是看过的缩略图留在这台设备的浏览器缓存里，别人只有知道完整地址或翻查缓存文件才能看到。
- 原图直链在存储域名下，不归本站清，拿到后 24 小时内仍可打开。
- 退出只删本设备的 Cookie。退出前被复制走的 Cookie 到期前仍然有效；要让所有设备退出，仍需改密码或用两步验证脚本。

原始证据（测试副本内）：`output/go-tests.jsonl`、`output/go-race.jsonl`、`output/ui-run.log`、`output/ui-results.json` 和截图。

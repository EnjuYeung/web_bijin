# 验收记录

## 测试环境说明

- 日期：2026-08-18
- 本机 macOS + Colima Docker，镜像 `bijin:local`，单容器 `0.0.0.0:5001->5001/tcp`
- 开发验证另用 `go run` 监听 `:5002`（`AUTH_USER=juen` `AUTH_PASS=changeme` `TZ=Asia/Shanghai`）
- 照片夹 `./photos`：58 张可显示图 + 坏图 / 非图片
- 验证工具：`go test`、`curl`、Playwright（桌面 1280×800、手机 390×844）

## 测试方法说明

按 `TESTING.md` 顺序：启动 → 核心流程 → 异常 → 边界 → 持久化 → 回归 → 构建部署。  
接口用 `curl` 对证据；页面用 Playwright 真机点击、读 DOM、截图。不靠读代码宣布通过。

## 测试结果说明

### T1 启动
- T1.1 Compose 启动：通过。容器 `healthy`，日志 `scan done files=59`，`ready=58`。
- T1.2 健康检查：通过。`/api/health` 未登录即可 200，含 `"tz":"Asia/Shanghai"`。
- T1.3 缺账号不启动：通过。`go test` 中 `TestLoadConfigRequiresAuth`：空 `AUTH_USER`/`AUTH_PASS` 返回错误。

### T2 核心流程
- T2.1 未登录进不了首页：通过。`GET /` → 302 `/login?next=%2F`。Playwright 新会话打开 `/` 落在登录页。
- T2.2 错密码：通过。JSON 登录返回 401，无 Cookie；页面提示「用户名或密码不对。」仍停在 `/login`。
- T2.3 正确登录：通过。`juen` / `changeme` 后进首页，标题 `Juen's`，`58 张`。
- T2.4 黑夜模式按钮：通过。右上角是一颗 `button.theme-btn`。点一次：`自动`→`黑夜`，背景 `rgb(20, 28, 36)`，`localStorage=night`。再点：`白天`，背景 `rgb(197, 206, 214)`。
- T2.5 卡片 Hover Preview：通过。桌面悬停第一张：`scale=1.07`，`z-index=4`，底部 Overlay 三行可见（例：`36.jpg` / `2026年8月18日` / `JPEG · 400×340 · 3 KB`）。字号随卡片约 `13.44px`。截图 `output/playwright/desktop-hover.png`。
- T2.6 大图信息栏（桌面默认开）：通过。点图后右侧栏展开，字段：文件名、格式、大小、分辨率、像素、存储空间。`aria-expanded=true`。点「收起」后 `display:none`，按钮改回「信息」。
- T2.7 大图信息栏（手机默认关）：通过。390×844 打开大图：`meta-off`，栏 `display:none`，按钮为「信息」。点开后栏出现且「收起」可点，再点收回。
- T2.8 登录页背景：通过。桌面 CSS 使用 `/api/login-bg?orient=land`，抽到的图宽>高（例 430×340）。手机使用 `orient=port`，抽到的图高>宽（例 400×420）。
- T2.9 点大图返回：通过。点原图关闭浮层，hash 清空，瀑布流仍在。

### T3 异常 / 不得绕过
- T3.1 未登录列表：通过。`GET /api/photos` → 401。
- T3.2 未登录缩略图 / 原图：通过。`/thumb/1`、`/original/1` → 401。
- T3.3 直接要 `index.html`：通过。`GET /index.html` → 404。
- T3.4 新浏览器会话：通过。清 Cookie 后再开 `/` 仍是登录页，进不了瀑布流。

### T4 边界
- T4.1 横竖背景回退：通过。单元测试 `splitByOrient`：有横图时 land 只抽横图，有竖图时 port 只抽竖图；正方形进后备池。
- T4.2 低占用：通过。Docker `CPU 0.00%`，内存 `2.176MiB`。
- T4.3 手机两列：通过。截图 `output/playwright/mobile-waterfall.png`。

### T5 持久化
- T5.1 主题：通过。点「黑夜」写入 `localStorage=night`。
- T5.2 会话密钥：通过。`TestLoadSessionKeyPersists` 同一数据目录两次读到同一把 key。
- T5.3 篡改 Cookie：通过。改 MAC 或过期会话一律无效。

### T6 回归
- 健康检查仍公开，Compose 能变 `healthy`。
- 点大图仍能关；左右翻页按钮仍在。
- 虚拟滚动仍只挂视口附近卡片（桌面约 24 个 `.sheet`，不是 58）。

### T7 构建
- `docker build -t bijin:local .` 成功（本机 `credsStore=desktop` 缺可执行文件，改走 Colima socket 构建）。
- `docker-compose up -d` 单容器，容器内 5001，环境里有 `AUTH_USER=juen`。

### 单元测试
- `go test ./...` 通过，含鉴权、登录背景取向、元数据格式、随机分页。

## 测试结论说明

第二版在本机 Docker 和浏览器里可用：登录不能绕过；主题是一颗按钮；桌面卡片悬停放大并出三行信息；大图右侧信息栏桌面默认开、手机默认关，且可用按钮折叠；登录页按桌面横图 / 手机竖图随机抽背景。

未测：N100 / Unraid 真机；真实相机照片做登录背景时的观感（当前夹具多为纯色块，背景看起来像色块而不是风景照）；18:00 之后自动切黑夜的整点跳变。

登录账号当前 Compose 默认是 `juen` / `changeme`，请在 `.env` 里改成自己的。

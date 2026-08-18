# 验收记录

## 测试环境说明

- 时间：2026-08-18 18:40:12 CST
- 本机 macOS，Go `1.26.6 darwin/arm64`，Docker Compose 单容器 `bijin:local`，端口 `0.0.0.0:5001->5001/tcp`
- 隔离开发实例监听 `:5002`，照片夹 `./photos` 共扫描 59 个文件，其中 58 张可显示、1 张坏图被跳过
- 浏览器：Playwright Chromium，桌面 `1280×800`、手机 `390×844`
- 验证工具：Node 语法检查、`go test`、`go vet`、Go 构建、Docker Compose、`curl`、Playwright DOM/截图/控制台

## 测试方法说明

按 `TESTING.md` 的启动、核心流程、异常、边界、持久化、回归、构建部署顺序执行。后端用实际命令和 HTTP 状态验证；前端在真实 Chromium 中登录、点击、Hover、切换主题、开关信息卡，并同时读取元素坐标和计算样式。所有截图保存在 `output/playwright/`。

## 测试结果说明

### T1 语法、单元测试与构建

- 状态：通过。
- 证据：`node --check web/app.js`、`go test ./...`、`go vet ./...`、`go build ./...` 均成功；`go test` 输出 `ok bijin`。
- 覆盖：鉴权、会话密钥、随机分页、路径、图片元数据和登录背景方向等现有测试未回归。

### T2 Hover Preview 收敛

- 状态：通过。
- 操作：桌面 Chromium 悬停首张卡片，悬停前后分别读取卡片矩形、层级、图片 transform 与 metadata opacity。
- 证据：卡片前后都为 `x=6`、`y=62.390625`、`w=308.75`、`h=255.046875`，`scale=none`、`z-index=1`；内部图片从 `matrix(1)` 变为 `matrix(1.025)`，metadata 从 `opacity=0` 变为 `1`。卡片没有位移、变大或遮挡相邻照片。
- 截图：`third-pass-home-desktop.png`、`third-pass-hover-desktop.png`。

### T3 图形主题按钮

- 状态：通过。
- 操作：点击图形按钮循环主题，并检查 DOM、可见 SVG、无障碍名称和本地存储。
- 证据：自动切到黑夜时 `data-mode=night`、`data-theme=dark`、`localStorage=night`；继续切到白天时只有 `theme-icon-day` 可见，按钮名称为「显示模式：白天；点击切换」，隐藏文本为「白天」。
- 截图：`third-pass-night-desktop.png`、`third-pass-home-mobile.png`。

### T4 大图与信息卡（桌面）

- 状态：通过。
- 操作：1280×800 点开照片，确认桌面默认展开信息卡，再点击图形按钮收起。
- 证据：信息卡 `display=block`、尺寸 `304×779.21875`、圆角 `18.4px`；按钮 `aria-expanded=true`、名称「收起信息」，且只显示收起面板图标。收起后信息卡消失，按钮变为信息图标。
- 折叠图标路径为 `M15 3v18M7 9l3 3-3 3`，箭头朝右侧屏幕边缘；实际点击后信息卡向右收起。Playwright 控制台 `Errors: 0, Warnings: 0`。
- 大图文件名已变成绝对定位的左下角胶囊，不再参与图片居中布局。
- 截图：`third-pass-lightbox-desktop.png`、`third-version-collapse-right.png`。

### T5 手机布局与信息卡

- 状态：通过。
- 操作：390×844 打开首页和大图，检查瀑布流列数、横向溢出、信息卡默认状态、开关和尺寸。
- 证据：首页列坐标为 `x=6/198`，两列宽均为 `186px`，`body.scrollWidth=390` 无横向溢出；可视区只渲染 20 张卡片。大图信息卡默认 `meta-off` / `display=none`；点击信息图标后卡片为 `304×826.40625`，四周留白，内容无需内部滚动。Escape 可关闭大图并回到瀑布流。
- 截图：`third-pass-lightbox-mobile-closed.png`、`third-pass-lightbox-mobile-open.png`、`third-pass-home-mobile.png`。

### T6 登录、异常与浏览器控制台

- 状态：通过。
- 证据：全新浏览器会话访问 `/` 落到登录页；输入 `juen` / `changeme` 后进入 58 张照片的首页。Playwright 完整流程后控制台 `Errors: 0, Warnings: 0`。
- `curl`：未登录 `GET /` 为 302，未登录 `GET /api/photos` 为 401；登录为 200，登录后首页为 200，列表 `limit=1` 返回 1 张且 `total=58`。
- 截图：`third-pass-login-desktop.png`。

### T7 Compose 部署与资源占用

- 状态：通过。
- 证据：`docker compose build` 成功；`docker compose up -d` 重建单容器后状态 `healthy`。`/api/health` 返回 `ok=true`、`seen=59`、`ready=58`、`tz=Asia/Shanghai`。
- 日志只包含预期的坏图跳过警告，没有未处理错误。
- 空闲快照：CPU `0.00%`，内存 `2.223MiB`。

## 测试结论说明

第三版 UI 优化通过本机真实浏览器、接口、单元测试和 Compose 部署验收：Hover 不再移动或放大卡片；主题与信息控制均为可访问的图形按钮；展开状态的折叠箭头与信息卡实际向右收起的方向一致；桌面和手机的信息卡行为正确；夜樱色、圆角和星标让界面更柔和，但没有增加运行依赖或常驻资源。

未测试：Intel N100 / Unraid 真机；真实相机照片下的最终主观观感；18:00 整点自动从白天切到黑夜的长时间等待场景。

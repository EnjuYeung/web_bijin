# 代码文件导览

这份文档用于帮助第一次接触项目的人快速了解各文件的职责。项目是一个单进程 Go 应用：启动后扫描照片目录和对象存储、维护 SQLite 索引、缩略图和随机壁纸，支持网页向本地或现有 S3 上传图片、接收对象存储的上传通知，并同时提供登录、接口、公开的随机壁纸接口和前端页面。

## 建议阅读顺序

1. `README.md`：了解项目用途和使用方法。
2. `ARCHITECTURE.md`：了解技术栈、数据流和主要设计。
3. `main.go`、`config.go`：了解程序如何启动。
4. `source.go`、`scan.go`、`events.go`、`store.go`、`thumb.go`、`wallpaper.go`：了解照片如何进入图库并生成壁纸。
5. `server.go`、`auth.go`、`upload.go`：了解页面、接口和登录。
6. `frontend/components/album-app.tsx`、`frontend/components/photo-gallery.tsx`、`frontend/app/globals.css`：了解浏览器界面。

## Go 后端

| 文件 | 作用 |
|---|---|
| `main.go` | 程序入口。初始化配置、SQLite、扫描器、缩略图、登录、页面服务（:5001）和可选的上传通知服务（:5002），并处理安全退出。 |
| `config.go` | 读取环境变量，整理监听端口、照片目录、数据目录、时区、扫描间隔、缩略图并发数和上传通知（端口与令牌）等配置。 |
| `storage.go` | 对象存储配置：字段校验与默认值（Endpoint、Bucket、前缀、Region、寻址方式、大图直连与浏览器访问地址），以及 SQLite `storages` 表的增删改查与补列迁移。 |
| `settings.go` | 设置页接口：读取本地说明、各来源状态、上传通知状态与壁纸统计、添加/修改/删除对象存储、测试连接；Secret Key 只写不读。 |
| `server.go` | 注册 HTTP 路由，提供照片、相册、缩略图（不可变缓存）、原图（转发或 302 到预签名地址）、登录背景（302 到一张壁纸）、公开随机壁纸和健康检查接口，并内嵌前端文件。 |
| `upload.go` | GUI 上传：同源与路径校验、现有目标、本地原子提交、S3 条件预签名与保存验证、进程内任务状态，以及复用扫描器的索引/缩略图/壁纸处理。 |
| `auth.go` | 校验用户名和密码，生成签名 Cookie，保护需要登录的页面和接口。 |
| `store.go` | 管理 SQLite 照片索引，包括建表、查询、更新、按来源计数和删除；数据库权限收紧为 0600。 |
| `source.go` | 图片源集合：本地目录与多个 S3 兼容存储的列举、读取和 Stat，按索引键分派，通知按桶与前缀匹配存储，大图预签名（按版本复用），Region 自动识别、连接测试和中文错误提示。 |
| `scan.go` | 定时或按需逐个扫描图片源，边列举边并发处理新增、变化、删除和损坏的图片，缩略图提交后接着生成壁纸、给缺壁纸的旧照片补生成；单个来源失败只保留它自己的索引，扫描期间新加的照片不被清理。 |
| `events.go` | 上传通知：校验令牌后立即应答，后台按 key 向存储 Stat 当前状态再入库或删除，同 key 合并、临时失败重试，并记录通知状态。 |
| `thumb.go` | 在有限的并发名额内读取并完整验证图片（每张照片一把锁），应用 EXIF 方向，原子保存按来源版本区分的 JPEG 缩略图，并把解码好的图片交给壁纸生成；删除照片时一并删掉它的缩略图和壁纸文件。 |
| `wallpaper.go` | 随机壁纸：规格与方图规则、按 Sharp 方式取整的缩放、WebP 编码与原子保存、壁纸表的读写与统计、公开的 `/v1/backgrounds/random` 和 `/media/sha256/…webp`、跨域预检。 |
| `meta.go` | 处理照片标题、格式和日期等通用信息。 |
| `order.go` | 实现带种子的随机排序和游标分页，保证同一次浏览中的顺序稳定。 |
| `album.go` | 按照片所在文件夹聚合相册，生成相册名称、数量、封面并过滤相册照片。 |
| `genphotos.go` | 生成本地测试照片和异常文件，不参与正式程序构建。 |

## 前端

| 文件 | 作用 |
|---|---|
| `frontend/app/layout.tsx` | Next.js 页面外壳、元数据和首屏主题/侧栏偏好脚本，不请求用户数据。 |
| `frontend/app/page.tsx`、`frontend/app/login/page.tsx` | 首页与登录页静态导出入口。 |
| `frontend/components/album-app.tsx` | 标题、导航、网站标题折叠交互、按需加载设置页、上传页与视图选择。 |
| `frontend/components/photo-gallery.tsx` | 游标分页、虚拟瀑布流、hash 大图、键盘/触摸切换、焦点和滚动恢复。 |
| `frontend/components/albums.tsx` | 文件夹相册封面、数量、空态与重试。 |
| `frontend/components/settings-panel.tsx` | 来源卡片、校验表单、添加/编辑/测试/删除对象存储、大图直连与浏览器访问地址、上传通知状态、随机壁纸卡片（数量与可复制的接口地址）；扫描中短时刷新。 |
| `frontend/components/upload-panel.tsx` | 独立上传页：目标选择、文件/文件夹添加、拖放、多任务进度、处理状态、停止与重试；文件夹只取直属图片。 |
| `frontend/lib/upload.ts` | 文件选择与非递归目录处理、格式/大小筛选、XHR 传输进度、取消和等待处理。 |
| `frontend/components/login.tsx` | 登录表单、错误提示与安全返回地址。 |
| `frontend/components/ui/` | 官方 shadcn/ui Base UI 组件；统一语义颜色、按钮状态、表单与对话框。 |
| `frontend/lib/api.ts`、`frontend/lib/preferences.ts` | 现有 Go API 的类型与请求、格式化、主题与侧栏偏好。 |
| `frontend/app/globals.css` | 令牌到 Tailwind / shadcn 的映射、各页面布局、响应式与减少动态规则。 |
| `tokens.css` | 主图库与登录页共享的颜色、字体、间距、动效、圆角和侧栏尺寸；在构建时合并进静态 CSS。 |
| `frontend/next.config.ts`、`frontend/scripts/export.mjs` | Next.js 静态导出、生成 JavaScript / CSS 的 gzip 副本，并复制到 Go 内嵌目录 `web/`。 |
| `frontend/package.json`、`frontend/package-lock.json`、`frontend/components.json` | 前端依赖、可复现安装与 shadcn 配置。 |

## 测试

| 文件 | 作用 |
|---|---|
| `album_test.go` | 测试文件夹相册的分组、命名、封面和过滤。 |
| `auth_test.go` | 测试登录、Cookie、访问保护和相关 HTTP 行为。 |
| `assets_test.go` | 验证导出资源的门禁边界，以及 gzip 解压内容、MIME、缓存与拒绝压缩时的回退。 |
| `frontend/tests/album.spec.ts`、`frontend/playwright.config.ts` | Playwright 实际浏览器回归：登录、照片/相册、大图、设置表单（含大图直连：浏览器跟随 302 直接向存储取原图）、主题/品牌交互与响应式、随机壁纸卡片与复制、别的网站无 Cookie 引用壁纸、登录页背景为壁纸。 |
| `frontend/tests/s3_fixture.py` | 仅监听本机的 S3 浏览器测试服务，使用生成的测试图片，接受预签名读写、条件写入与任务元数据，并按 `response-*` 参数返回响应头，不连接真实桶。 |
| `upload_test.go` | 上传核心验收：保存/索引/缩略图/壁纸、身份与来源检查、非法路径/图片、传输中断、并发同名不覆盖、符号链接、S3 现有凭据与坏图误报回归。 |
| `frontend/tests/upload.spec.ts` | 单/多图、本地/S3、文件夹直属图片、多文件夹拖入不读取子目录、失败重试、同名跳过，以及两种主题与 320–1440 px 布局。 |
| `scan_test.go` | 测试照片扫描、格式识别和异常文件处理。 |
| `order_test.go` | 测试随机排序和游标分页。 |
| `meta_test.go` | 测试照片标题和格式。 |
| `path_test.go` | 测试路径安全，防止访问照片目录之外的文件。 |
| `source_test.go` | 测试来源键分派、多来源同名路径、单来源失败隔离、删除来源清理、V1/V2 列举、Region 识别、连接测试和错误提示。 |
| `settings_test.go` | 测试对象存储字段校验、SQLite 增删改查与 id 不复用、设置接口的登录保护、JSON 限制、Secret 不外泄和完整生命周期。 |
| `s3fake_test.go` | 测试用的最小 S3 服务：V1/V2 列举、Region 查询、HEAD/GET、请求头或预签名参数的密钥检查和可调读取延迟。 |
| `events_test.go` | 上传通知：新建/覆盖/删除、真实 RustFS 报文解码、令牌与忽略规则、重试、同 key 合并；同一张图多路触发只读一次、扫描并发数、扫描期间新增照片不被清理。 |
| `direct_test.go` | 大图直连：302 与预签名参数、同版本复用与新版本换地址、未登录拒绝、默认地址可下载、关闭直连时转发；存储字段与旧表迁移；设置接口；并发与通知配置。 |
| `testdata/rustfs-event-*.json` | 从 RustFS 1.0 实际抓到的上传、删除、复制通知（身份信息已脱敏）。 |
| `review_fix_test.go` | 审查问题回归：失败恢复、坏图、缓存、并发、会话、S3 超时、EXIF、持久化和索引迁移。 |
| `wallpaper_test.go` | 随机壁纸：规格与取整（用 Sharp 实际尺寸）、生成→接口读取→替换→删除的完整生命周期、参数与 204/400、随机均匀、补生成只读一次、壁纸失败不影响相册、上传通知、登录背景、只公开两个路径、设置统计、旧库迁移。 |
| `TESTING.md` | 项目的测试原则和验收顺序。 |
| `TEST_REPORT.md` | 最新一次实际测试的环境、方法、结果和结论。 |

## 构建与部署

| 文件 | 作用 |
|---|---|
| `Dockerfile` | Node 静态导出 → Go 内嵌编译 → 精简 Alpine 单进程运行镜像。 |
| `docker-compose.example.yaml` | Docker Compose 部署示例（netcup + 1Panel）：`1panel-network`、只绑定本机的端口、目录挂载、环境变量、资源上限和健康检查。 |
| `.env.example` | 环境变量示例。 |
| `go.mod`、`go.sum` | Go 版本和第三方依赖清单。 |
| `.dockerignore` | 控制 Docker 构建时不进入上下文的文件。 |
| `.gitignore` | 控制不提交到 Git 的本地数据、照片、测试输出和部署配置。 |

## 项目文档

| 文件 | 作用 |
|---|---|
| `README.md` | 面向使用者的安装和操作说明。 |
| `ARCHITECTURE.md` | 当前技术栈、架构、功能和接口说明。 |
| `DECISIONS.md` | 记录重要技术和设计选择及其原因。 |
| `design.md` | Hallmark 锁定的界面设计系统，约束主图库、登录页和大图层共享的视觉语言。 |
| `CHANGELOG.md` | 记录各版本完成功能。 |
| `AGENTS.md` | 约束后续开发、测试、文档和部署工作的项目规则。 |

## 运行时目录

这些目录不是核心源码，通常不会提交到 Git：

- `photos/`：本地测试或实际照片来源；添加对象存储后仍与对象存储一起扫描。
- `data/`：SQLite（照片索引、壁纸表与对象存储配置）、会话密钥、缩略图缓存和壁纸（`wallpapers/`）。
- `output/`：浏览器截图等测试产物。
- `web/`：Next.js 静态导出产物（仅 `.gitkeep` 提交）；Go 构建前必须生成。
- `frontend/node_modules/`、`.next/`、`out/`：安装依赖与构建缓存。

更新时间：2026-10-06 14:07:51 CST。

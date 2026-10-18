# 最近一次测试报告

2026-10-18 23:22:28 CST：功能卡片去掉说明文字、数量改为图标加数字、上传去掉目标目录等界面改动的开发验收。测试对象是提交 `ade2962`（基线 `f219362`）：最后一轮把这个提交导出到 netcup 的全新目录，用新生成的测试图片整体重跑。之后只改了文档。

## 测试环境

- netcup Debian 13，新建隔离目录 `/root/bijin-test-20261018`（`git archive` 导出，0 个 `._*` 文件），不连接生产数据库、照片、RustFS 或真实 AI 服务；测试期间生产容器没有改动或重启（healthy）。
- Go：`golang:1.27-alpine`（`-tags nodynamic`，race 另装 build-base）；前端与浏览器：`mcr.microsoft.com/playwright:v1.63.0-noble`（Node、Chromium、WebKit，Next.js 16.3.7）。
- 浏览器测试在容器自己的网络里运行：测试服务 `output/bijin-ui-test`（127.0.0.1:18092 / 18094）、`tests/s3_fixture.py`（18093）、`tests/recognizer_fixture.py`（18095），测试图片由 `genphotos.go` 新建。

## 测试方法与结果（最后一轮，提交 `ade2962`）

| 编号 | 方法 | 结果 |
| --- | --- | --- |
| T01 | `npm ci && npm run typecheck && npm run build`；`tsc --noEmit --noUnusedLocals --noUnusedParameters` | 通过；未使用检查 0 项 |
| T02 | `npm run test:organizer / test:upload / test:names / test:recognition` | 15 / 21 / 3 / 6 通过，0 失败 |
| T03 | `gofmt -l`、`go vet`、`staticcheck`（2026.2.2）、`deadcode` | 全部无输出 |
| T04 | `go test -tags nodynamic -count=1 -json .` | 179 个顶层测试通过、0 失败；`TestQueryScale` 可选压测按默认跳过 |
| T05 | 同上加 `-race` | 179 通过，0 处 DATA RACE |
| T06 | 干净数据上全量 `npx playwright test` | Chromium / WebKit 共 127 项通过，0 失败，6.6 分钟 |

## 业务验收

| 编号 | 用户想做什么 / 预期 | 证据 | 状态 |
| --- | --- | --- | --- |
| U01 | 设置页四个子页都没有副标题、卡片底栏和说明折叠；对象存储表单里只剩「高级设置」可展开，展开后出现 Region | settings 01、settings 08 | 通过 |
| U02 | 「本地图库」卡片里已没有按钮，键盘从子页按钮按 Tab 仍能进入这张卡片，并看到焦点框 | settings 03 | 通过（首轮失败后修复） |
| U03 | 整理页「整理相册」标题旁是相册图标和「4」，悬停为「共 4 本相册」；筛选后为「2 / 4」；列表表头不再有计数 | organize 01、organize 07 | 通过 |
| U04 | 名单标题旁是作者、模特的图标与人数（「作者 1 位」「模特 2 位」）；有 3 本失效记录时多一个错误色「3」，展开后仍有清理提示；名单展开后没有说明句 | organize 01、organize 10 | 通过 |
| U05 | 网格视图里一本相册名字很长、换成好几行时，320 / 375 / 768 / 1024 / 1440 五种宽度下同一排卡片的名称、作者框、模特框顶端都对齐 | organize 09（含截图） | 通过 |
| U06 | 上传页没有子目录输入框、副标题和格式说明；「保存位置」在宽屏与标题同一行、右边和拖入区对齐，窄屏在标题下方占满一行；没有提示时下方提示行不占位置 | upload 06（两种浏览器、两种主题、五种宽度） | 通过（首轮失败后修复） |
| U07 | 单张、多张图片存进「根目录」相册且原图字节一致；文件夹存成最外层同名相册、子文件夹跳过；多个文件夹拖入；中断重试；同名跳过不覆盖；S3 直传 | upload 01–05、upload-recovery 全部 | 通过 |
| U08 | 接口收到带 `directory` 的上传请求时拒绝（400），不写任何文件；准备请求只带 `target`、`path`、`size` | Go `TestUploadRejectsChildFoldersAndInvalidPathsBeforeWriting`；上传批次单元测试 | 通过 |
| U09 | 相册页「管理分享」只显示链条图标、正方形按钮，名字留在悬停提示与读屏名称里，开关照常 | batch-sharing「sharing controls stay hidden…」及其余分享测试 | 通过 |
| U10 | 侧栏没有「家里的回忆」，退出登录贴在侧栏底部；登录页表单上方只有「Juen's」 | logout 01、logout 02 | 通过 |

## 失败、修复与复测记录

- 第一轮全量（提交 `3af9eab`）Chromium / WebKit 124 项通过、3 项失败：
  - settings 03（Chromium）：从子页按钮按 Tab 进不了「本地图库」卡片。原因是删掉的「修改本地照片目录」折叠是这张卡里唯一能获得焦点的东西。修改实现：「本地图库」子页本身可获得焦点（标签页模式的做法），测试没有改。
  - upload 06（两种浏览器）：宽屏时「保存位置」下拉比标题中线低 8.5 px。原因是卡片右上角的位置默认跨两行，给副标题留了一行。修改样式：上传卡片的这个位置只占标题那一行，测试没有改。
- 修复后先单独重跑 settings 与 upload 两个文件（21 项通过），再以提交 `ade2962` 在全新目录整体重跑（上表）。
- 原有测试的调整都跟随这次功能变化，没有删除或放宽检查：上传测试不能再填子目录，改用唯一的文件名或文件夹名隔离，仍核对原图字节与子文件夹未上传；整理页计数由「共 4 本」改为「共 4 本相册」，名单人数由一句话改为两个计数；退出登录的位置改为对照侧栏底边（原来对照已删除的标语）；上传批次单元测试由 `destination` 改为 `target`，并核对准备请求只有三个字段。
- 新增：浏览器 organize 09（长名称网格对齐）、organize 10（失效记录提示）；设置、上传、分享、退出登录测试里增加了对应界面的检查；Go 增加一个带 `directory` 的请求被拒绝的检查（在原有测试内）。

## 发布与部署（2026-10-18 23:27:03 CST 记录）

用户要求：测试通过后合并到 master 并发布，发布前照旧备份数据库、保留回滚镜像。

| 编号 | 实际操作 | 结果 |
| --- | --- | --- |
| D01 | `git push origin master` | GitHub master 为 `7e8864e`（功能提交 `ade2962` 加验收记录） |
| D02 | SQLite 在线备份到 `/root/bijin-backup-20261018-1723-predeploy.db`（服务器时间，CST 23:23），`quick_check`，记录各表行数；当前镜像另打标签 `bijin:rollback-20261018-predeploy` | 单个文件、0600、quick_check ok。photos 6758、photo_shares 6759（503 个已开启）、album_people 157、people 46、wallpapers 6806、storages 1、share_batches 3、share_batch_items 455、organize_receipts 82、recognizer 1 |
| D03 | 部署目录 `git pull --ff-only` 到 `7e8864e`，`docker compose build && docker compose up -d` | 新镜像 `f67f9c2221cf` healthy，重启 0 次；启动扫描 6758 张、0 失败；日志只有 INFO |
| D04 | 部署后只读核对数据库 | quick_check ok；各表行数与部署前完全一致，503 个分享与识别服务配置保留，数据库仍为 0600 |
| D05 | 公网核对 `https://csb.jgbman.cc` | health 200；登录页 200、标题「Juen's」、不含「家里的回忆」；未登录访问首页 302 到登录页；`/api/settings`、`/api/albums`、`/api/photos`、`/api/upload-targets`、`/api/share-batches`、`/api/recognizer`、`/api/people` 与未登录的上传准备都是 401；随机壁纸 302 到 `/media/sha256/…webp`，取回 200 image/webp |
| D06 | 随机抽 3 个已开启的分享链接 HEAD 与 GET；再试一个不存在的地址；检查部署后的程序 | 3 个都是 200（2 张 JPEG、1 张 PNG），GET 字节数与记录一致；不存在的地址 404。程序里已没有「目标子目录」「家里的回忆」「修改本地照片目录」「支持哪些存储服务」，新界面的标记都在；部署后无 WARN / ERROR |

保留：本次部署前数据库备份、回滚镜像 `bijin:rollback-20261018-predeploy`，以及上一次的备份与 `bijin:rollback-20261017-predeploy`。测试目录、Go 与 Playwright 镜像和构建缓存已删除。

## 限制与结论

- 视觉核对依据测试截图（上传页桌面与手机、整理页网格长名称、登录页、侧栏、设置页、相册页顶栏），没有在 iPhone 真机上看。
- 登录后的新界面没有在生产上用真实账号点开看过（需要登录），以测试截图和程序内容核对代替；请在手机和电脑上各看一眼设置、整理、上传和相册页。
- 结论：提交 `ade2962` 的构建、静态检查、Go（含 race）与两种浏览器的全量回归全部通过；已推送并部署生产，生产 healthy，数据与 503 个分享保留，部署前备份与回滚镜像已保留。

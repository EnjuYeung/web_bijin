# 最近一次测试报告

2026-10-19 00:30:41 CST：修复整理页网格在 Firefox 下的问题——封面和相册名之间有大片空白、勾选框和识别按钮掉到卡片下部。测试对象是提交 `e4f7ec6`（基线 `c525de0`）：最后一轮把这个提交导出到 netcup 的全新目录，用新生成的测试图片整体重跑。之后只改了文档。

## 测试环境

- netcup Debian 13，新建隔离目录 `/root/bijin-test-20261019`（`git archive` 导出，0 个 `._*` 文件），不连接生产数据库、照片、RustFS 或真实 AI 服务；测试期间生产容器没有改动或重启（healthy）。
- Go：`golang:1.27-alpine`（`-tags nodynamic`，race 另装 build-base）；前端与浏览器：`mcr.microsoft.com/playwright:v1.63.0-noble`（Node、Chromium、WebKit、Firefox，Next.js 16.3.7）。
- 浏览器测试在容器自己的网络里运行：测试服务 `output/bijin-ui-test`（127.0.0.1:18092 / 18094）、`tests/s3_fixture.py`（18093）、`tests/recognizer_fixture.py`（18095），测试图片由 `genphotos.go` 新建。

## 复现

| 编号 | 条件 | 结果 |
| --- | --- | --- |
| R01 | 4 本测试相册，封面尺寸改成线上常见的几千像素，Chromium / WebKit，375 与 1440 宽 | 不出现，封面与名字间距 8 px |
| R02 | 40 本相册（长名称、多层目录、多位模特、已配置识别服务），封面改成竖图，五种宽度，滚动前后 | Chromium / WebKit 不出现 |
| R03 | 同 R02，直接以网格打开（记住的视图），加 Firefox | 只有 Firefox 出现：间距 514–603 px，第一行高度等于缩略图自身的 720 px |
| R04 | Firefox 下试三种只改样式的写法 | 两种无效；`contain: size` 让封面压到名字上（-198 px） |
| R05 | Firefox 下试封面外包方框 / 方框带比例 / 按容器宽度算高度 | 三种都修好，Chromium / WebKit 不受影响；选外包方框 |

修复后的截图中又发现：Firefox 下勾选框和识别按钮在卡片下部（勾选框比封面顶端低 309 px），一并修复。

## 测试方法与结果（最后一轮，提交 `e4f7ec6`）

| 编号 | 方法 | 结果 |
| --- | --- | --- |
| T01 | `npm ci && npm run typecheck && npm run build`；`tsc --noEmit --noUnusedLocals --noUnusedParameters` | 通过；未使用检查 0 项 |
| T02 | `npm run test:organizer / test:upload / test:names / test:recognition` | 15 / 21 / 3 / 6 通过，0 失败 |
| T03 | `gofmt -l`、`go vet`、`staticcheck`（2026.2.2）、`deadcode` | 全部无输出 |
| T04 | `go test -tags nodynamic -count=1 -json .` | 179 个顶层测试通过、0 失败；`TestQueryScale` 可选压测按默认跳过 |
| T05 | 同上加 `-race` | 179 通过，0 处 DATA RACE |
| T06 | 干净数据上全量 `npx playwright test` | Chromium / WebKit / Firefox 共 129 项通过，0 失败，6.8 分钟 |

## 业务验收

| 编号 | 用户想做什么 / 预期 | 证据 | 状态 |
| --- | --- | --- | --- |
| U01 | 整理页切到网格（或直接以网格打开），封面下面紧接着相册名，不留空白（≤ 12 px） | organize-grid 01，三种浏览器、五种宽度、封面加载前后 | 通过 |
| U02 | 勾选框在封面左上角、识别按钮在封面右上角 | 同上 | 通过 |
| U03 | 名字长短不一时，同一排卡片的名字、作者框、模特框仍对齐；封面比例不变（宽屏 4:3，手机 3:4） | 同上；organize 08 | 通过 |
| U04 | 列表视图的小封面、整理页其他功能不受影响 | organize 01–08、10，recognition 全部 | 通过 |

## 失败、修复与复测记录

- 新测试先在未修复的代码上跑：Firefox 失败（空白 591 px；加入角落按钮检查后，勾选框偏下 309 px），Chromium / WebKit 通过。修复后三种浏览器都通过。
- 第一轮全量（提交 `2384caf`）127 项通过、2 项失败，都是新测试自己的问题：写死了「4 本相册」，而全量时上传测试先留下了相册（WebKit 看到 7 本，Firefox 看到 10 本）。按项目约定改为从服务器取相册数，并逐张滚动让懒加载的封面都加载；「每一行都要有多张卡片」改为「至少有一行多张卡片」（总数为奇数时最后一张可以单独一行）。检查项没有放宽。之后以提交 `e4f7ec6` 在全新目录整体重跑（上表）。
- 原 organize 09（只在 Chromium 跑的网格对齐测试）移入新的 `organize-grid.spec.ts`，并增加 Firefox、竖图大尺寸封面、识别服务、直接以网格打开和角落按钮的检查。

## 发布与部署（2026-10-19 00:36:16 CST 记录）

用户确认 23:38 的 1Panel 镜像清理是本人操作，并要求用新做的备份和回滚镜像发布。

| 编号 | 实际操作 | 结果 |
| --- | --- | --- |
| D01 | `git push origin master` | GitHub master 为 `ff2da79`（修复提交 `e4f7ec6` 加验收记录） |
| D02 | 发布前发现 10-17、10-18 两份部署前数据库备份和两个回滚镜像都已不在服务器上（回滚镜像由 CST 23:38 的 1Panel「清理容器 [镜像]」删除，释放 67.74 MB）。重新在线备份到 `/root/bijin-backup-20261018-1831-predeploy.db`（服务器时间，CST 00:31），当前镜像打标签 `bijin:rollback-20261019-predeploy` | 单个文件、0600、quick_check ok。photos 6758、photo_shares 6759（503 个已开启）、album_people 157、people 46、wallpapers 6806、storages 1、share_batches 3、share_batch_items 455、organize_receipts 82、recognizer 1 |
| D03 | 部署目录 `git pull --ff-only` 到 `ff2da79`，`docker compose build && docker compose up -d` | 新镜像 `4eda428a8652` healthy，重启 0 次；启动扫描 6758 张、0 失败；日志只有 INFO |
| D04 | 部署后只读核对数据库 | quick_check ok；各表行数与部署前备份完全一致，503 个分享保留，数据库仍为 0600 |
| D05 | 公网核对 `https://csb.jgbman.cc` | health 200；登录页 200、标题「Juen's」；未登录访问首页 302 到登录页；7 个需登录的接口都是 401；随机壁纸 302 后取回 200 image/webp |
| D06 | 随机抽 3 个已开启的分享链接；不存在的地址；检查部署后的程序 | 3 个都是 200（2 张 JPEG、1 张 PNG），字节数与记录一致；不存在的地址 404；程序里已有新的封面方框样式和角落按钮定位；部署后无 WARN / ERROR |

保留：本次部署前数据库备份与回滚镜像 `bijin:rollback-20261019-predeploy`（注意：1Panel「清理镜像」会把它当作未使用的镜像删除）。测试目录、Go 与 Playwright 镜像和构建缓存已删除。

## 限制与结论

- 没有在用户自己的 Firefox 版本上直接验证，用的是 Playwright 1.63 自带的 Firefox；也没有在 iPhone 真机上看。
- 结论：提交 `e4f7ec6` 的构建、静态检查、Go（含 race）与三种浏览器的全量回归全部通过；已推送并部署生产，生产 healthy，数据与 503 个分享保留，部署前备份与回滚镜像已保留。请在自己的 Firefox 里打开整理页网格看一眼。

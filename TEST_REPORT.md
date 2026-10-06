# 查询扩容验收记录

## 测试环境说明

- 时间：2026-10-06 22:16:13 CST。基于 master 的 39ff1d0 开发；新增 album_path 与两个部分索引、分页/相册/壁纸/统计缓存，保持单机、单进程和单个 SQLite 连接。没有修改前端或既有浏览器用例。
- netcup：Debian 13、AMD EPYC 9645；Go 1.27.1 Alpine、Node 24 Alpine；Go 回归和压测容器限制 6 核/4 GiB。新构建镜像 bijin:query-test 仅用于隔离验收。
- 本地辅助验证：macOS amd64、Go 1.27.1、Node 26.0.0、Playwright 1.63.0；Chromium 153、WebKit 26.6。
- 数据：所有用例使用临时 SQLite、生成的照片或本机 S3 fixture；压测生成 100,000 / 200,000 条元数据与约 4/3 倍壁纸记录、1,000 本相册，不生成对应容量的原图，不连接生产桶。
- 生产容器与生产数据库没有部署或迁移；正式启用新版本后才会执行 schema 补列和回填。隔离运行容器测试结束后已删除。

## 测试方法说明

1. 先完成前端 npm ci、typecheck 和静态构建，再运行 Go 回归。netcup 最终代码运行 go test -tags nodynamic -count=1 ./... 和 go vet -tags nodynamic ./...。
2. 新增 12 项专项验收：旧顺序与游标、字段迁移和回滚旧版后回填、实时详情、目录/损坏/删除失效、来源统计、人物变更、索引命中、计数不等待数据库、缓存并发与上限、壁纸概率和过期候选、淘汰后续页。
3. 本地完整竞态检查通过（176.491 秒）；最终统计改动后，重新运行 query 专项及相关统计的竞态检查，通过（25.506 秒）。
4. BIJIN_QUERY_SCALE=1 go test -tags nodynamic -run '^TestQueryScale$' -count=1 -v：真实 HTTP 入口先核对照片数量与字段、相册数量/封面及壁纸内容，再逐入口运行 5 并发、250 次请求。深分页从随机序列 90% 位置继续；检查热态前后缓存构建次数相同。
5. 使用最终二进制运行 Chromium 全套；另运行 WebKit 非剪贴板用例，保留其工具限制的失败记录。
6. netcup 构建完整 Docker 镜像，在 127.0.0.1:18095 的独立容器和独立目录验收登录、原图、缩略图、壁纸哈希、数据库字段/索引及重启持久化；不替换生产容器。

## 测试结果说明

| 编号 | 功能、操作与预期 | 方法与实际证据 | 状态 |
|---|---|---|---|
| Q01 | 启动与旧库迁移：历史目录正确，新/旧版切换后能补齐空字段 | TestQueryMigrationBackfillsDirectoriesAndRollbackWrites；根目录、中文目录、旧版漏写新列均正确 | 通过 |
| Q02 | 瀑布流：固定 seed 与旧实现一致，分页不重复遗漏、淘汰后继续 | TestQueryPagesPreserveLegacyOrder、TestQueryOrderEvictionRebuildsSameCursor，含负 seed 与 int64 上界 | 通过 |
| Q03 | 并发变化：详情实时，损坏/移目录/删除不返回旧候选 | TestQueryVisibleScopeDetailsAndInvalidation、TestQueryPageSkipsStaleCandidatesAndFillsPage；跳过后填满本页并推进游标 | 通过 |
| Q04 | 相册与人物：数量、最早时间、最新封面、关联正确 | TestQueryAlbumSummaryAndPeopleInvalidation；人物变更不重建照片汇总，删除封面后正确替换 | 通过 |
| Q05 | 壁纸：均匀按照片抽取，规格/尺寸过滤和版本校验正确 | 9,000 次抽样，3 张照片各选中次数落在 2,600–3,400；方图不获双倍权重；过期候选不返回 | 通过 |
| Q06 | 缓存：有容量限制、不重复构建，不发布过期版本 | TestQueryCachesSingleBuildStalePublishAndLimits、并发读写；热计数在唯一 DB 连接被占用时立即返回 | 通过 |
| Q07 | 设置统计：待处理不算坏图，损坏与修复后刷新 | TestQuerySourceCountsPendingBrokenAndRecovered；壁纸统计的两个读取共用短读事务，竞态通过 | 通过 |
| Q08 | 查询计划：ID 和相册查询命中目标索引 | TestQueryPlansUseIndexes 验证 photos_visible_id、photos_visible_album_time 和 INTEGER PRIMARY KEY | 通过 |
| Q09 | Go 回归与构建 | 共 106 个测试定义；常规回归中 105 个执行、1 个显式压测跳过；随后压测独立执行通过。netcup 回归 52.945 秒，vet 通过，Docker 镜像构建成功 | 通过 |
| Q10 | Chromium 实际操作：登录、瀑布流/大图、相册整理、设置、本地/S3 上传 | 最终二进制 19/19 通过，92.975 秒；涵盖异常输入、重试、响应式与两种主题 | 通过 |
| Q11 | WebKit 回归 | 18 项执行，17 项通过；第 06 项在 CDP 手势调用处失败，第 07 项剪贴板按工具适用范围未执行，详见下文 | 部分验证 |
| Q12 | 隔离容器运行与重启持久化 | private /api/photos=401；登录后 1 张照片/1 本相册；原图 23,395 B、缩略图 5,275 B、壁纸 2,680 B；原图逐字节一致、壁纸 SHA-256 一致；重启 ID 与 Cookie 持续有效，album_path 与索引存在 | 通过 |

5 并发热态 HTTP 的 P95（同机内部请求，ms）：

| 元数据条数 | 照片首段 | 相册列表 | 随机壁纸 JSON | 90% 位置深分页 |
|---:|---:|---:|---:|---:|
| 100,000 | 6.148 | 9.603 | 3.073 | 6.004 |
| 200,000 | 5.895 | 11.079 | 2.976 | 6.048 |

冷态首次构建耗时（ms）：100,000 条照片 167.4、相册 47.8、壁纸 366.5；200,000 条照片 347.1、相册 67.5、壁纸 686.9。首次打开新 seed、失效或淘汰后仍有构建成本，不能以热态数据代替冷态。

内存：100,000 条保留 Go 堆 15.00 MiB，200,000 条 28.67 MiB；200,000 条测试进程 RSS 105,164 KiB（约 102.7 MiB），峰值 109,640 KiB（约 107.1 MiB）。不是生产空闲内存，也不含实际大图下载/解码/编码时的峰值。

WebKit 限制：现有 album.spec.ts 第 06 项使用 browserContext.newCDPSession / Input.dispatchTouchEvent，WebKit 报 CDP session is only available in Chromium；书签、关闭、浏览器前进后退的前置断言已通过，手势步骤未验证。第 07 项使用 Chromium 的剪贴板权限而未执行。原前端和原用例均未修改；Chromium 中对应两项通过。不能把这两项记为 WebKit 通过。

证据：隔离 netcup 的 go-final.log、docker-build.log、runtime-smoke.json/log；本地 output/go-race.log、query-race-final.log、query-test.log、ui-chromium-final.json、ui-webkit-final.json。测试日志为临时产物，不提交源代码仓库；核心命令、结果和限制保存在本报告中。

## 测试结论说明

查询改动通过 Go 回归、专项与竞态、Chromium 19 项、WebKit 17 个适用项、10 万/20 万元数据压测及隔离容器实际运行与重启验收。热态 P95 均低于预定 100 ms 目标，后段翻页未随分页位置增加成本，热请求没有重建全量索引/汇总。

WebKit 的 CDP 手势和剪贴板两项未完整验证；真实手机 Safari 手势、公网/CDN 传输、400 GiB 原图导入与编码吞吐不在本次已验证范围。代码和文档按要求提交 master 并推送 Git；生产部署留给启用新版本时执行。

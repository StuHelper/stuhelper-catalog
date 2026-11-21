---
type: index
status: Accepted
updated: 2026-11-21
---

# StuHelper Catalog

课程目录与可信选课记录模块，提供 Go 服务、PostgreSQL Repository、HTTP Handler 和生成的 Go/TypeScript SDK。catalog 拥有规范目录、历史、作息、本人选课和搜索来源。学校接入协议与来源数据由私有适配器维护。

课程、教师、学期、学期开课、教学班、授课关系和排课分别保存。公开课程列表按课程去重，支持开课学院、类别、性质及教师筛选。客户端课程列表不展示学期；学期保留在资格证据和课程评价中。

导入每页最多 500 个对象与选课记录，事务中一起保存历史、审计和 outbox。失败或遗漏记录不会删除旧数据。来源修订防止旧数据覆盖，人工更正继续保留。保留的导入回执支持超过 24 小时重试，相同公开字段的新批次只推进来源修订。

学生选课接口只读取认证请求人的记录。应用可注入可信 EnrollmentSource，在本人读取前提交最多 2000 条的完整来源快照。快照事务停用已移除的同来源记录，来源错误保留数据并返回不可用。规则见[本人选课来源](docs/spec/enrollment-source.md)。

普通目录展示课程、教师、学期及分类，保留课程与教师的双向关联。学期开课、教学班、授课关系和排课仅供本人接口、管理端及受信模块使用。列表请求受限类型返回 403，详情、摘要和解析返回 404，批量 lookup 将受限 ID 列入 missing_ids。

公开课程与教师摘要返回学校、院系、分类、课程数与教师数，省略 offering_count。管理端摘要保留开课数。目录及筛选数量仍按可见课程或教师去重，规则见[可展示目录投影](docs/spec/directory-projection.md)。维护写入口先校验维护权限与学校范围，人工更正支持清除未知字段，拒绝修改已合并来源别名或写入与对象类型无关的字段，并使用修订号拒绝并发覆盖。

学校检索支持完整标识码、五位代码、名称与目录别名，规则见[学校匹配](docs/spec/audit-discovery.md#学校代码与名称匹配)。

目录维护支持合并与停用。合并先预览并核对修订，目标资料继续使用，旧 ID、曾用课程号、曾用名及历史保留。公开查询解析旧关联，评价统计按合并后的课程去重。目标曾用名容量不足时，预览拒绝合并，避免丢弃源名称。停用不会删除历史，目录默认隐藏停用对象。

规范化导入支持差异预览，区分新增、更正、保留人工值、冲突与未关联记录。预览使用回滚事务；确认令牌同时绑定输入和当时数据，过期预览拒绝写入。学校抓取、定时同步和来源任务仍由共享接入能力负责。

公共目录详情、搜索、批量引用、摘要和合并解析均要求当前内容准入。应用通过 ReadAccess 提供服务学校与当前资格，缺少依赖时拒绝读取。本人选课只按登录和归属授权，管理员使用独立的 /catalog/admin 读接口并检查维护范围。

## 使用与检查

先维护 `contracts/catalog.openapi.json`，再执行 `make generate`。应用注入认证结果与可维护学校范围，不能由客户端指定操作人。SQL 仅位于 postgres 包。

```sh
make setup
bash scripts/check-with-postgres.sh
```

检查脚本创建仅通过 Unix socket 访问的临时 PostgreSQL 集群，退出时清理。Catalog checks 在 GitHub `ubuntu-26.04` 上使用同一入口，检查生成一致性、模块边界、文档、权限、并发与投影规模。

迁移身份执行 `postgres.Migrate`；运行身份仅获得 catalog schema 的读写权限，不得创建数据库对象或访问其他模块 schema。`postgres.CheckRuntime` 在应用装配时检查权限。

生产发行、验证范围与待验证事项见[目录实施与验证](docs/development/catalog.md)。

目录事件通过业务事务内的 outbox 交给 backend relay，投递、确认、排除退避主题和重建见[审计与搜索来源](docs/spec/audit-discovery.md)。

[module.yaml](module.yaml) 登记模块依赖、可用能力及场景测试。platform 提供基础能力，identity 支持目录读写和本人选课授权。增强目录另需 reviews 与 favorites 的指标端口，resources 批量提供本人最近课程的资料数量。

[文档导航](docs/README.md) · [课程与评价规格](https://github.com/StuHelper/stuhelper/blob/develop/docs/spec/academic-content.md) · [目录维护规格](https://github.com/StuHelper/stuhelper/blob/develop/docs/spec/catalog-operations.md)

本人名称查询 /catalog/me/references 只返回本人当前或历史选课记录关联对象的 ID、类型和名称，单次最多 100 个 ID。任一 ID 不属于本人记录时拒绝整次请求。名称接口不返回简介、评分、统计或排课详情，公共目录仍检查内容准入。

## 本人课表投影

MySchedule 是供模块使用的公开接口，只读取当前账号的有效选课记录和相应教学班排课。使用批量名称与排课查询，不查询其他账号记录，也不将内容准入失效放宽到公共目录。

每页最多 100 个选课记录，每条最多 100 个排课，一页排课最多 1000 条。超过范围返回 unavailable，调用方减小分页后重试。没有排课时返回空 meetings，不据此判断用户空闲。

结构化排课、学校学期作息、本人 HTTP 课表和 iCalendar 导出见[实现规格](docs/spec/my-courses.md)。导入预览同时返回解析结果与原文，未知时间保持缺失。

## 审计与搜索来源

公开审计查询、catalog.discovery.v1 事件、当前权限批量查询和含墓碑的重建快照见[接口规格](docs/spec/audit-discovery.md)。backend 使用公开 Go 接口装配 platform 与 search，不能跨模块读取 catalog 表。

热门活动来源、日快照、计数规则与清理接口见[活动来源规格](docs/spec/activity.md)。

课程与教师的游标分页、分类与名称索引、存量迁移和性能验证见[目录筛选规格](docs/spec/directory-performance.md)。查询诊断保留固定阶段与原因，不记录搜索词或对象身份。

教师公开目录保留具备有效授课、官网匹配或获批独立主页资料任一依据的教师。完整来源刷新、并发重放、详情与搜索过滤见[教师目录标记](docs/spec/teacher-directory.md)。产品 Repository 启用 RequireTeacherDirectory 后，缺少标记的教师不进入公开查询。

原型目录的分面、排序、本人最近课程和下一上课日见[原型数据接口](docs/spec/ui-data.md)。教师目录依赖 T2 持久标记，热度及推荐默认值已确认。

产品启用 RequireDirectoryProjection 后，课程、教师、筛选、摘要与搜索使用版本化投影。截止学期默认 2023 秋，旧课程在详情及公开入口返回不存在。重建、导入中断、配置切换和资源上限见[可展示目录投影](docs/spec/directory-projection.md)。

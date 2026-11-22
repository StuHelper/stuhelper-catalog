---
type: spec
status: Accepted
updated: 2026-11-21
---

# 热门活动来源

## 协议与数据归属

活动采用 search 的 discovery-activity.schema.json 2.0.0。contracts 保存原始 Schema、来源提交和 SHA-256。测试核对固定摘要，再逐条验证 outbox 事件，拒绝未拥有的指标字段。

catalog 只发送 views。事件类型为 discovery.activity.v2，source 和 target.source 均为 catalog。活动 ID 为 kind/base64url(规范对象 ID)/北京时间日期，base64url 不带填充，subject 等于活动 ID。

每个对象日维护独立单调修订。事件完整替换日快照，零值显式发送；零计数墓碑不会被迟到事件重新计数。事件、计数和修订历史在同一业务事务提交，并调用 pg_notify('catalog_outbox', '')。

日快照只含目标、日期、修订、可见性和本模块指标，不含账号、正文、身份原值或支付信息。每项公开计数为 0 至 1000000000，越界会返回错误并回滚本次计数写入。

## 浏览计数与读取取舍

公开 Read 和 ReadSummary 成功返回课程或教师详情时计数。列表、批量引用、内部事实读取、管理读取和 CurrentActivity 不计数。同一已登录账号、同一规范对象、同一北京时间日期只计一次。通过别名读取时使用规范对象 ID 去重。

view_dedup 只保存每日独立密钥生成的 HMAC-SHA256 令牌及日期。令牌只保留当日，PruneViewDedup 每次最多删除一千条过期令牌，已累计的 views 保留。

清理同时推进不可回填日期下界。即使服务时钟回退，已清理日期也不能再次累计。目录停用、合并、投影规则变化或教师标记变化会逐批修订相关日快照的 Visible，恢复时保留原计数，不转移其他对象的计数。

统计写入单独使用最多 250 毫秒的事务。失败会回滚摘要、计数、修订及 outbox，详情读取仍返回已获准的数据；之后同日再次读取可以重试。因此浏览数是尽力记录，故障期间可能少计，不阻断内容读取。

失败日志只包含 operation=activity.view 和 unavailable、timeout 或 cancelled 原因，不记录账号、对象、SQL 或错误原文。Service.ViewMetrics() 提供 Recorded、Duplicates、Failures 三个并发安全累计指标，供宿主导出。

运行身份另需 view_dedup 的读取与追加、view_retention 的读取，以及 view_day_keys 的读写和过期删除。去重清理身份只需 view_dedup 删除和 view_retention 更新权限，不读取日密钥。

## 公开接口与一致性

postgres.Repository.ActivitySnapshot(ctx, fromDay, toDay, cursor, limit) 返回 []Activity、next、error。窗口左闭右开，边界传北京时间零点的绝对时刻（UTC 前一日 16:00）。limit 为 1 至 100。首轮 cursor 为空，next 为空表示结束。

分页绑定窗口、上限和 PostgreSQL 可见性快照，读取不可变历史中的首轮最新修订，包含零计数和不可见墓碑。分页期间的新增、修改及延迟提交不能进入旧快照。游标有效五分钟，窗口或上限变化会被拒绝。

Service.CurrentActivity(ctx, principal, activityID) 返回 Activity、bool、error。请求人由可信认证提供，每次检查当前目标权限；明确不可读或不存在返回 false，数据库和来源依赖故障返回错误，不表示来源未启用。

零计数快照仍可返回 true，Visible=false 表示该快照不贡献热度。当前查询不读取其他模块表；catalog 通过自己的公开读规则判断，其他来源通过所属模块公开接口取得当前目标事实。

统计目标按写入时的规范 ID 绑定。目录合并不搬运旧目标的历史计数，当前查询拒绝已经变成别名的旧目标。后续新增使用新的规范 ID，避免把不可证明的历史贡献复制到另一个对象。

OutboxChannel 为 catalog_outbox。PendingOutbox(ctx, limit, excludeSubjects) 每批最多 100 条，最多排除 1000 个主题，排除在数据库 LIMIT 前完成。ConfirmOutbox(ctx, id) 在消费者持久提交后调用，重复确认保留首次 sent_at。

内容事件和活动事件共用同一 outbox，relay 按 type 分发。backend 负责 LISTEN、启动和重连补读、60 秒兜底、退避及待处理主题；模块不扫描业务表寻找待投递变化。

## 清理、权限与验证

PruneActivityHistory(ctx, beforeDay, limit) 以北京时间零点为截止，每次最多删除 1000 条旧修订，保留每个对象日的最新修订和墓碑。清理在同一事务提高保留修订号，已有分页游标失效，调用方须中止重建。

服务端口 `PruneSentOutbox(ctx, maxAge, limit)` 删除超过指定时间且已确认的事件，`PruneReplays(ctx, limit)` 删除已到期维护回执。每次最多 1000 条，保留待投递事件及导入、选课刷新和课程身份合并的持久回执。

运行身份需要 activity_buckets 的读写、activity_history 的读取和追加、activity_retention 的读取，以及 outbox 的读写确认权限。历史删除权限只授予负责清理的身份。当前日快照和最新墓碑不自动删除。

活动快照和当前权限检查使用可信 Go 端口。backend 将 catalog 的活动快照适配为 search 来源，连同目录事件交给统一 relay。CAT-ACTIVITY-01 的隔离 PostgreSQL 用例登记在模块清单。

| 场景 | 验证条件 |
| --- | --- |
| CAT-ACTIVITY-01 | 本来源计数、权限、并发、回滚、事件 Schema、固定分页、墓碑和清理上限 |
| CAT-ACTIVITY-02 | 北京时间零点、UTC 16:00 前后、跨月、1 日及 366 日窗口。旧日令牌、密钥和热度游标过期 |
| CAT-ACTIVITY-03 | PostgreSQL 按已知时刻或时长比例归日，重复执行保留各日计数，回退保留新增浏览 |

## 存量归日与回退

迁移用保留的 outbox 事件时间和活动修订恢复可确认的浏览增量。剩余 UTC 日汇总按两个北京时间日期的相交时长分配，完整 UTC 日为 16 比 8。切换当天的 UTC 日期只按已过时段分配。次日四舍五入，当日保留余数。

迁移保留浏览总量。估算部分可能改变单日计数和窗口边缘的热度。

`postgres.Migrate` 生成更高修订的日快照并使旧快照游标失效。`postgres.RollbackActivityCalendar` 恢复 UTC 日期和对应事件，保留切换后的新增计数。匿名分段总量供两个日历投影重建使用，运行身份仅通过触发器累计。

每次切换以更新的完整快照替换旧日历待发的活动事件。已确认的活动事件及其他类型的待发事件继续保留。

切换及回退期间暂停应用和 relay 写入，使用迁移身份操作。切换后用当前提供方快照重建搜索热门排行，再恢复读取。

## 日密钥与最近访问

view_day_keys 只保存日期和独立随机密钥。跨日令牌不同。backend 每五秒有界删除过期日密钥，每批最多一千条。密钥删除后，仍待清理的旧令牌也无法用该密钥重新匹配。

北京时间切换前后的令牌使用不同的 HMAC 消息前缀。旧令牌缺少可用于重新归日的事件时间，按日期到期清理。切换或回退当天，同一账号再次访问同一对象时可能重复计数。本人最近课程使用独立表和九十天保留规则，见[目录与本人课程摘要](ui-data.md)。

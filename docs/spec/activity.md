---
type: spec
status: Accepted
updated: 2026-11-09
---

# 热门活动来源

## 协议与数据归属

活动采用 search 9c56ad3 的 discovery-activity.schema.json，版本 1.0.0。contracts 保存原始 Schema、来源提交和 SHA-256。测试核对固定摘要，再逐条验证 outbox 事件，拒绝未拥有的指标字段。

catalog 只发送 views。事件类型为 discovery.activity.v1，source 固定为 catalog，target.source 固定为 catalog。活动 ID 为 kind/base64url(规范对象 ID)/UTC 日期，base64url 不带填充，subject 等于活动 ID。

每个对象日维护独立单调修订。事件完整替换日快照，零值显式发送；零计数墓碑不会被迟到事件重新计数。事件、计数和修订历史在同一业务事务提交，并调用 pg_notify('catalog_outbox', '')。

日快照只含目标、日期、修订、可见性和本模块指标，不含账号、正文、身份原值或支付信息。每项公开计数为 0 至 1000000000，越界会返回错误并回滚本次计数写入。

## 浏览计数与读取取舍

公开 Read 和 ReadSummary 成功返回课程或教师详情时计数，列表、批量引用、内部事实读取、管理读取和 CurrentActivity 不计数。同一已登录账号、同一规范对象、同一 UTC 日只计一次；通过别名读取也使用规范对象 ID 去重。

view_dedup 只保存账号与对象组合的 SHA-256 摘要和 UTC 日期。保留当前日及之前 31 日。PruneViewDedup(ctx, now, limit) 每次最多清理 1000 条，清理不修改已累计 views。

清理同时推进不可回填日期下界。即使服务时钟回退，已清理日期也不能再次累计。目录停用或合并会逐批修订相关日快照的 Visible，恢复时保留原计数，不转移其他对象的计数。

统计写入单独使用最多 250 毫秒的事务。失败会回滚摘要、计数、修订及 outbox，详情读取仍返回已获准的数据；之后同日再次读取可以重试。因此浏览数是尽力记录，故障期间可能少计，不阻断内容读取。

失败日志只包含 operation=activity.view 和 unavailable、timeout 或 cancelled 原因，不记录账号、对象、SQL 或错误原文。Service.ViewMetrics() 提供 Recorded、Duplicates、Failures 三个并发安全累计指标，供宿主导出。

运行身份另需 view_dedup 的读取与追加、view_retention 的读取。去重清理身份需要 view_dedup 删除和 view_retention 更新权限。

## 公开接口与一致性

postgres.Repository.ActivitySnapshot(ctx, fromDay, toDay, cursor, limit) 返回 []Activity、next、error。日期必须为 UTC 零点，窗口左闭右开，limit 为 1 至 100。首轮 cursor 为空，next 为空表示结束。

分页绑定窗口、上限和 PostgreSQL 可见性快照，读取不可变历史中的首轮最新修订，包含零计数和不可见墓碑。分页期间的新增、修改及延迟提交不能进入旧快照。游标有效五分钟，窗口或上限变化会被拒绝。

Service.CurrentActivity(ctx, principal, activityID) 返回 Activity、bool、error。请求人由可信认证提供，每次检查当前目标权限；明确不可读或不存在返回 false，数据库和来源依赖故障返回错误，不表示来源未启用。

零计数快照仍可返回 true，Visible=false 表示该快照不贡献热度。当前查询不读取其他模块表；catalog 通过自己的公开读规则判断，其他来源通过所属模块公开接口取得当前目标事实。

统计目标按写入时的规范 ID 绑定。目录合并不搬运旧目标的历史计数，当前查询拒绝已经变成别名的旧目标。后续新增使用新的规范 ID，避免把不可证明的历史贡献复制到另一个对象。

OutboxChannel 为 catalog_outbox。PendingOutbox(ctx, limit, excludeSubjects) 每批最多 100 条，最多排除 1000 个主题，排除在数据库 LIMIT 前完成。ConfirmOutbox(ctx, id) 在消费者持久提交后调用，重复确认保留首次 sent_at。

内容事件和活动事件共用同一 outbox，relay 按 type 分发。backend 负责 LISTEN、启动和重连补读、60 秒兜底、退避及待处理主题；模块不扫描业务表寻找待投递变化。

## 清理、权限与验证

PruneActivityHistory(ctx, beforeDay, limit) 每次最多删除 1000 条旧修订，保留每个对象日的最新修订和墓碑。清理在同一事务提高保留修订号，已有分页游标失效，调用方须中止重建。

运行身份需要 activity_buckets 的读写、activity_history 的读取和追加、activity_retention 的读取，以及 outbox 的读写确认权限。历史删除权限只授予负责清理的身份。当前日快照和最新墓碑不自动删除。

接口均为公开 Go 端口，OpenAPI 路径和生成客户端没有变化。GOWORK=off 的隔离 PostgreSQL 检查覆盖修订单调、事务回滚无通知、Schema、固定快照、墓碑、并发和有界清理。backend 装配及实际热门页面组合另行验证。

| 场景 | 验证条件 |
| --- | --- |
| CAT-ACTIVITY-01 | 本来源计数、权限、并发、回滚、事件 Schema、固定分页、墓碑和清理上限 |

---
type: spec
status: Accepted
updated: 2026-11-09
---

# 审计查询与搜索来源

## 审计查询

postgres.Repository.ReadAudit 接收 auditquery.Request，返回 auditquery.Batch。backend 先完成认证和当前授权交集计算，再把 platform 的 Scopes、筛选、Before 和 Snapshot 映射到本接口。接口属于受信服务间调用，不直接接收客户端自报的授权范围。

Request 的 Actor、Target、Action 为精确筛选，时间范围为 From 包含、Until 不包含。多个 Scope 按并集处理，每个 Scope 同时检查 School、Group 和 Module。catalog 记录的 Module 为 catalog，Group 为 *；具体群授权不能读取学校级目录操作。

范围与所有筛选在数据库分页前执行。Limit 为 1 至 101，兼容 platform 额外读取一条的行为。排序为时间、来源、ID 严格降序，字符串按字节比较。Before 可来自其他来源，方便 platform 合并各模块结果。

Snapshot 使用 PostgreSQL 事务可见性和审计保留修订。后插入的回填记录及首轮查询时尚未提交的记录均不能进入快照。审计更新、删除或清空会使旧快照失效并返回错误。令牌最长 256 字节，无法完整编码时返回不可用，不重建快照掩盖遗漏。

统一字段为 ID、Source、Actor、Target、Action、Scope、Reason、Result、CreatedAt。只查询显式审计列，不返回 data、身份材料、正文或导入载荷。Actor 为认证系统提供的稳定主体键，不能传入原始身份材料。

Reason 使用 catalog.merge 或 catalog.state_change 操作分类码，其余返回空字符串。管理员自由文本原因继续保留在原有私有业务记录中，不进入公开摘要。业务成功事务明确写入 success；未声明结果时保存并返回 unknown，不根据事件类型推测成功。

审计表直接定义最终字段，新增写入同事务保存原因、结果、操作者、对象、操作、学校和时间。本轮不提供旧表兼容或历史回填。部署使用最终 Schema 的新环境，已有数据库的升级由负责人另行安排。

## 搜索来源事件

contracts/discovery-event.schema.json 保存 search 来源协议。每次课程或教师持久化均在业务事务中追加 discovery_history，并写 catalog.discovery.v1 outbox 和 pg_notify('catalog_outbox', '')。其他目录类型不发出该事件。

事件使用 CloudEvents 1.0，source 为 catalog，subject 等于 data.target.id，ID 由对象 ID 与修订确定。data 包含 target、revision、title、school_id、visible、ratings 和 at。catalog 不拥有评分事实，因此 ratings 五项均为零。

可见文档只使用规范 ID。合并时为源 ID 提高修订并发出不可见墓碑，同时为目标提高修订并发出新快照。墓碑保留原 ID，以便删除 search 中旧条目。墓碑标题为空，不再提供旧公开标题。

Delete 按现有目录保留历史规则执行逻辑删除，等价于 SetState 的 Inactive=true。删除发出更高修订墓碑；管理员恢复时再提高修订。没有物理删除目录和历史记录的公开接口。旧导入修订、重复幂等请求和预览不重复发布事件。

## 当前可见性与重建

Service.DiscoveryCurrent 接收已认证 Principal 和 1 至 100 个 ID，返回当前可见 DiscoveryDocument，包含 ID 与修订。每次检查当前内容准入和学校，过滤停用对象、旧别名、其他学校及未知 ID。撤销准入返回空列表，依赖故障返回错误。

postgres.Repository.DiscoverySnapshot 是受信重建接口，接收 cursor 和 1 至 100 的 limit，返回 Items 与 Next。空 cursor 开始快照，空 Next 表示结束。所有课程和教师均包含在快照中，包括不可见墓碑。

分页以 ID 字节序递增，从不可变投影历史中读取首轮 PostgreSQL 可见性对应的最新修订。分页期间的导入、修改、合并、删除及迟提交事务不改变该快照。游标有效期五分钟，历史修改或清理会使游标失效，调用方必须中止此次重建。

投影历史与墓碑不自动清理，避免迟到事件使删除内容复活。清理前须由负责人确定来源重放期限和存储上限；清理后旧快照返回错误。分页不保存服务端会话或完整数据副本，单页最多读取 101 条结果。

## 装配与验证

backend 将 ReadAudit 适配到 admin.AuditProvider，将 DiscoveryCurrent 和 DiscoverySnapshot 适配到 search.Sources。snapshot 的 Next 原样传回，文档字段直接映射。当前查询必须传递可信请求人，不能只信搜索索引的 visible。

catalog.OutboxChannel 为 catalog_outbox。postgres.Repository 实现 OutboxReader，提供 PendingOutbox(ctx, limit) 和 ConfirmOutbox(ctx, id)。所有目录业务事件、catalog.discovery.v1 和作息变更共用 Envelope、同事务写入与通知路径。

PendingOutbox 的 limit 为 1 至 100，仅读取 sent_at IS NULL。部分索引与查询均按 source、subject、数值 revision、ID 升序排列，字符串按字节比较。读取不锁定或确认事件，重复读取保留相同 ID 和完整载荷，空队列返回空列表。

ConfirmOutbox 在消费者持久提交后调用，设置首次 sent_at。重复或并发确认保留该时间，不存在的 ID 返回 not_found。sent_at 是唯一投递状态；数据表直接定义最终结构，不提供旧投递状态的兼容或转换路径。

relay 按 (source, subject) 串行消费，前一事件失败时不能越过它投递同组后续事件。多个 relay 的协调由 backend 承担。消费成功但确认失败时允许重投，消费者须按事件 ID 实现 inbox 去重。

relay 收到 LISTEN 通知立即调用 PendingOutbox，启动及重连时补读，每 60 秒兜底读取。失败退避和 24 小时待处理队列由 backend 按 EVENT-DELIVERY.md 装配。业务表不作为增量轮询来源。

| 场景 | 验收结果 |
| --- | --- |
| CAT-AUDIT-01 | 筛选、范围并集、跨来源游标、稳定分页、空原因、unknown 和脱敏 |
| CAT-DISCOVERY-01 | 逐对象事件、事务回滚、重复请求、合并与删除墓碑、当前权限过滤 |
| CAT-DISCOVERY-02 | 更新与迟提交不改变快照，分页无重复遗漏，历史清理使快照失效 |
| CAT-OUTBOX-01 | 事务通知、积压补读、批量上限、数值顺序、重复读取与幂等确认 |
| CAT-OUTBOX-02 | discovery 和作息载荷完整保留，墓碑字段不丢失，消费后确认 |

本地隔离 PostgreSQL 18.6 的完整 make check 通过，包含 go vet、竞态测试、SDK 一致性、边界检查、构建及文档检查。backend 适配器、search 组合重建和真实账号尚未验收。本轮没有新增 HTTP 路径，OpenAPI 与 HTTP SDK 保持现有定义。

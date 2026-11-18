---
type: spec
status: Accepted
updated: 2026-11-18
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

目录发布时，从新版本的 `projection_documents` 读取学校、课程和教师的搜索文档。增量事件与全量重建使用同一份文档，因此学期截止、名称与分类排除、体育项目归并、曾用课号隐藏和教师展示条件同时生效。

发布事务将新增和变化的文档写入 outbox，切换目录版本并通知 `catalog_outbox`。曾经可见的对象退出目录时，事件保留对象 ID，清空标题和检索字段，并标记为不可见。文档内容相同则沿用原修订，重新发布不会重复发送。

`catalog.discovery.v1` 遵循 contracts/discovery-event.schema.json。CloudEvents 的 source 为 catalog，subject 等于 data.target.id，事件 ID 由发布版本和对象 ID 确定。修订来自目录版本。

学校和课程可携带 code 和 aliases，课程还可携带英文名、曾用号和曾用名。评分由其他模块维护，ratings 五项均为零。

学校、课程和教师的源对象写入仍在业务事务内追加 discovery_history，供未启用目录投影的读取路径使用。首次发布前和批次导入期间，保存源对象只更新来源事实。预览、失败批次和重复导入均不会把未发布文档送入搜索。

## 当前可见性与重建

Service.DiscoveryCurrent 接收已认证 Principal 和 1 至 100 个 ID，按当前准入和学校返回已发布的可见文档及修订。撤销准入返回空列表，依赖故障返回错误。

产品启用 RequireDirectoryProjection。DiscoverySnapshot 绑定首轮读取时各校的已发布版本，按 ID 字节序分页，单页最多 100 项。游标有效期五分钟，所需版本被清理后返回不可用。快照可包含曾经发布的对象墓碑。

未启用目录投影的来源快照按 discovery_history 和首轮 PostgreSQL 事务可见性分页。后续写入和迟提交事务不改变快照，历史被修改或清理后旧游标失效。

## 装配与验证

backend 将 ReadAudit 适配到 admin.AuditProvider，将 DiscoveryCurrent 和 DiscoverySnapshot 适配到 search.Sources。snapshot 的 Next 原样传回，文档字段直接映射。当前查询必须传递可信请求人，不能只信搜索索引的 visible。

catalog.OutboxChannel 为 catalog_outbox。postgres.Repository 实现 OutboxReader，提供 PendingOutbox(ctx, limit, excludeSubjects) 和 ConfirmOutbox(ctx, id)。

所有目录业务事件、catalog.discovery.v1 和作息变更共用 Envelope、同事务写入与通知路径。

PendingOutbox 的 limit 为 1 至 100，excludeSubjects 最多 1000 项，超限返回 invalid_input。nil 或空列表不排除主题。排除在数据库 LIMIT 之前执行，退避或人工处理中的主题不会占用返回批次；不存在或重复主题不影响结果。

查询仅读取 sent_at IS NULL 的部分索引，按 source、subject、数值 revision、ID 升序排列，字符串按字节比较。同主题的剩余事件保持原有顺序。读取不锁定或确认事件，排除主题保留待发送状态，空队列返回空列表。

ConfirmOutbox 在消费者持久提交后调用，设置首次 sent_at。重复或并发确认保留该时间，不存在的 ID 返回 not_found。sent_at 是唯一投递状态；数据表直接定义最终结构，不提供旧投递状态的兼容或转换路径。

relay 按 (source, subject) 串行消费，前一事件失败时不能越过它投递同组后续事件。多个 relay 的协调由 backend 承担。消费成功但确认失败时允许重投，消费者须按事件 ID 实现 inbox 去重。

relay 收到 LISTEN 通知立即调用 PendingOutbox，传入处于退避或人工处理的主题。启动及重连时补读，每 60 秒兜底读取。失败退避和 24 小时待处理队列由 backend 按 EVENT-DELIVERY.md 装配。业务表不作为增量轮询来源。

| 场景 | 验收结果 |
| --- | --- |
| CAT-AUDIT-01 | 筛选、范围并集、跨来源游标、稳定分页、空原因、unknown 和脱敏 |
| CAT-DISCOVERY-01 | 发布前无搜索事件，发布后的逐项事件与快照一致，覆盖事务回滚、重复请求、合并与删除墓碑、当前权限过滤 |
| CAT-DISCOVERY-02 | 更新与迟提交不改变快照，分页无重复遗漏，历史清理使快照失效 |
| CAT-OUTBOX-01 | 事务通知、积压补读、批量上限、数值顺序、重复读取与幂等确认 |
| CAT-OUTBOX-02 | discovery 和作息载荷完整保留，墓碑字段不丢失，消费后确认 |
| CAT-OUTBOX-03 | 排除阻塞主题后返回其他主题，顺序不变，空列表及容量边界正确，实际查询使用部分索引 |

本地隔离 PostgreSQL 18.6 的完整 make check 通过，包含 go vet、竞态测试、SDK 一致性、边界检查、构建及文档检查。backend 的隔离组合测试直接比较增量消费与全量重建后的搜索可见文档，并与目录课程 ID 逐项核对。

查询计划测试使用生产 SQL，在 20000 条已确认和 200 条待发送事件上运行 EXPLAIN ANALYZE。nil、空列表、部分排除、全部排除及 1000 项排除均使用 catalog_outbox_pending，未出现 Seq Scan。测试使用默认优化器，不关闭顺序扫描。

## 学校代码与名称匹配

kind=school 列表的 q 去除首尾空白。纯数字查询精确匹配学校 ID 或 code；恰好五位数字还匹配十位数字 ID/code 的后五位。例如 4111010006、10006 均可定位已登记的北京航空航天大学。其他长度不做数字子串匹配。

非纯数字查询对名称、code 和登记别名做不区分大小写的字面子串匹配。名称“北京航空航天大学”、别名“北航”或 Beihang 都依目录记录匹配；不推测未登记别名，百分号、下划线和反斜线不作为通配符。

查询在数据库分页前完成，继续按 ID 升序和排他 after 分页。不存在的代码返回空列表，停用与合并来源不返回。公开列表拒绝 include_inactive=true，公开详情、摘要、批量查询与解析均隐藏停用对象，管理员入口保留维护范围。

Filter.IDs 是可信模块的批量候选限制，最多 100 项；verification 通过宿主传入已启用认证学校 ID。此参数不从公开 HTTP 绑定，不替代调用方自己的资格与权限判断。

学校 code 与 aliases 随 discovery 事件和一致性快照送往 search。更名、别名修改、停用使用新修订，墓碑不保留名称和别名。CAT-SCHOOL-01 验证完整码、五位码、名称、别名、无结果、字面符号及 HTTP 与快照一致性。

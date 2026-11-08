---
type: spec
status: Accepted
updated: 2026-11-09
---

# 结构化课表与日历

## 排课与导入

schedule 对象的 schedule_time 包含 weekday（周一为 1）、start_period、end_period 和 weeks。节次范围为 1 至 30，周次范围为 1 至 60，周次集合不重复。单双周直接表达为周次集合。缺少结构化数据时，客户端显示“未提供”。

导入支持明确的结构化字段，也识别 `周一 第1-2节 第1-16周(单)` 格式。星期支持一至六、日和天，单双周后缀可省略。解析结果同时出现在导入预览中。无法解析的文本保留在 time_text，结构化字段保持缺失。所有原文均保留，显式无效结构返回 invalid_input。

## 本人查询与日历

GET /api/v1/catalog/me/schedule 必须传 term_id，仅查询认证请求人的有效选课，返回课程 ID 与名称、教学班 ID、教师 ID、地点、时间原文、结构化排课及可用作息。作息缺失时省略 timetable，客户端显示“未提供”。

课表分页上限为 100 条选课，每条最多 100 个排课，每页最多 1000 个排课。超限返回 unavailable，客户端可缩小分页。历史学期按 term_id 隔离。

GET /api/v1/catalog/me/schedule/calendar 导出该学期完整 iCalendar。最多处理 100 条选课和 10000 个事件，超限返回 unavailable。没有选课时返回空日历。任一选课缺少排课、结构化时间或所需作息时，返回 HTTP 422 schedule_incomplete，避免静默遗漏课程。

日历按星期和周次展开本地日期，使用学校 IANA 时区转为 UTC DTSTART 与 DTEND。夏令时造成的无效或歧义时间返回 schedule_incomplete。UID 由请求人、学校、学期、教学班、排课 ID 和周次确定，调整作息保持 UID。文本转义、CRLF 和按字节折行符合 iCalendar 输出要求。

## 学校学期作息

GET 和 PUT /api/v1/catalog/admin/schools/{school_id}/terms/{term_id}/timetable 要求 catalog 管理能力和学校维护范围。配置包含学期第一周周一日期、IANA 时区和各节次开始结束时间。节次按编号和时间递增，不允许重叠或跨午夜。

配置按学校和学期独立持久化。创建传 revision=0，更正传当前修订号。并发更正返回 conflict；紧接成功写入的相同旧修订请求返回原结果。不存在的配置返回 not_found。

作息写入、审计与 CloudEvents outbox 在同一事务提交，并通知 catalog_outbox。审计记录操作者、学校、对象和修订。sent_at IS NULL 的部分索引用于待发送读取。已有 delivered 事件在迁移中标记 sent_at，保留未投递事件。

## 验收与后续接入

| 场景 | 结果 |
| --- | --- |
| MC-01：本人有选课 | 返回课程、教学班、教师与结构化排课，无法读取他人课表 |
| MC-02：切换学期 | 查询与日历均按学期隔离 |
| MC-03：时间或作息缺失 | 查询保留原文，日历返回明确缺失结果 |
| MC-05：单双周与时区转换 | 展开正确日期，保持稳定 UID，正确转义和折行 |
| MC-06：管理作息 | 校验范围及修订，重复和并发写入不重复产生审计，事务失败回滚 |

已通过本地隔离 PostgreSQL 18.6 的生成一致性、仓库检查、文档检查、go vet、竞态测试、构建与工作流检查。运行身份只访问 catalog 数据，迁移身份单独执行 Schema。

上课提醒尚未接入通知模块。backend 可从 MySchedule 和 ExpandSchedule 获取确定的 UID、Start 与 End，结合用户提醒偏好生成通知任务。catalog.timetable.updated.v1 及 catalog.imported.v1 由 LISTEN/outbox relay 投递，通知模块以 inbox 去重后更新任务，撤销旧时间任务。

通知模块仍需处理提前量、渠道、用户退订及选课变化。relay 的重连补读、60 秒兜底、退避和待处理队列由 backend 按 EVENT-DELIVERY.md 装配。个人日程编辑与真实学校数据验收继续由对应任务处理。

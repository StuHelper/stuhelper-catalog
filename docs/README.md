---
type: index
status: Accepted
updated: 2026-11-21
---

# Catalog 文档

课程目录与本人选课、课表由 catalog 维护。模块接口、依赖与本地检查见[仓库 README](https://github.com/StuHelper/stuhelper-catalog/blob/develop/README.md)。

| 分类 | 主题 | 内容 |
| --- | --- | --- |
| 规格 | [可展示目录投影](spec/directory-projection.md) | 可见范围、并发导入与版本发布 |
| 规格 | [目录筛选与查询资源](spec/directory-performance.md) | 查询语义、索引、指标与性能测试 |
| 规格 | [目录与本人课程摘要](spec/ui-data.md) | 目录分面、热度、最近访问与本人摘要 |
| 规格 | [课程换号与独立英文名](spec/course-identity.md) | 课程标识、曾用号和来源映射 |
| 规格 | [本人选课来源与目录刷新](spec/enrollment-source.md) | 可信快照、范围和幂等回执 |
| 规格 | [结构化课表与日历](spec/my-courses.md) | 周次、学期作息、所有权与导出 |
| 规格 | [审计查询与搜索来源](spec/audit-discovery.md) | 授权查询、目录发布事件与 relay |
| 规格 | [热门活动来源](spec/activity.md) | 浏览去重、快照、墓碑和清理 |
| 实施 | [目录实施与验证](development/catalog.md) | 测试范围、生产发行与待验证事项 |

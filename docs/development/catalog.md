---
type: development
status: Accepted
updated: 2026-11-21
---

# 目录实施与验证

## 当前实现

Catalog 在独立 PostgreSQL schema 保存源目录、历史、本人选课与学期作息。课程公开查询使用已发布投影；本人接口按认证请求人读取选课、课表及关联名称。审计和搜索通过公开 Go 端口与持久 outbox 交给宿主。

现行源码支持目录分面、多选与学分区间筛选、人工更正字段重置、持久重建任务和防抖合并构建。导入或重建失败时继续读取旧版本。规则见[目录投影](../spec/directory-projection.md)与[目录接口](../spec/ui-data.md)。

## 验证

`module.yaml` 登记 PostgreSQL 场景，涵盖导入幂等、目录合并、学校权限、本人课表与日历、搜索墓碑和投影重建。完整检查在隔离集群中执行竞态测试以及无竞态插桩的规模基准，阈值见[性能规格](../spec/directory-performance.md)。

StuHelper v0.1.31 锁定 Catalog v0.1.15。该生产组合完成数据库迁移、加密备份隔离恢复和匿名验收。组件与源码提交见[正式组合](https://github.com/StuHelper/stuhelper/blob/v0.1.31/workspace.lock.json)。

## 待验证

当前 develop 的目录维护变更需在后续发行组合中验证。本人课程摘要仍依赖宿主装配独立授权，上课提醒仍待接入；对应权限和实际投递按功能验收。

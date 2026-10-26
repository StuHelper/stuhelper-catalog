# StuHelper Catalog

课程目录与可信选课记录模块，提供 Go 服务、PostgreSQL Repository、HTTP Handler 和生成的 Go/TypeScript SDK。学校接入协议与真实数据由私有适配器维护。

课程、教师、学期、学期开课、教学班、授课关系和排课分别保存。公开课程列表按课程去重，支持开课学院、类别、性质及教师筛选。客户端课程列表不展示学期；学期保留在资格证据和课程评价中。

导入每页最多 500 个对象与选课记录，事务中一起保存历史、审计和 outbox。失败或遗漏记录不会删除旧数据。来源修订防止旧数据覆盖，人工更正继续保留。学生选课接口只读取认证请求人的记录。

## 使用与检查

先维护 `contracts/catalog.openapi.json`，再执行 `make generate`。应用注入认证结果与可维护学校范围，不能由客户端指定操作人。SQL 仅位于 postgres 包。

```sh
make setup
bash scripts/check-with-postgres.sh
```

检查脚本建立独立、仅 Unix socket 可访问的 PostgreSQL 18 集群，退出时清理。测试使用独立数据库与运行身份，检查事务回滚、并发幂等、学院分类、教师归属及禁止越权访问。

迁移身份执行 `postgres.Schema`；运行身份仅获得 catalog schema 的读写权限，不得创建数据库对象或访问其他模块 schema。`postgres.CheckRuntime` 在应用装配时检查权限。

当前首次建库迁移已验证。存量生产迁移、学校持续同步、运行库备份和恢复演练仍待实际环境验收。outbox 已持久化，通用跨服务事件投递另行接入。

业务规则见 [课程与评价规格](https://github.com/StuHelper/stuhelper-next/blob/develop/docs/spec/academic-content.md)。

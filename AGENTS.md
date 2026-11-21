---
type: guide
status: Accepted
updated: 2026-11-21
---

# 工程约定

遵循[主工作区工程约定](https://github.com/StuHelper/stuhelper/blob/develop/AGENTS.md)。catalog 管理自身目录与选课事实，SQL 位于 `postgres`。公开目录仅展示准入允许的对象，本人课表与选课引用按认证请求人隔离。

先更新 OpenAPI，再执行 `make generate`。在 `module.yaml` 登记已实现场景及对应测试函数。验证使用独立运行与迁移身份，完整检查入口为 `bash scripts/check-with-postgres.sh`。测试使用合成教务记录，真实凭据和教务数据留在获准的私有环境。

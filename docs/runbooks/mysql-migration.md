# MySQL 迁移与失败恢复手册

Admin 使用内嵌 Goose migration 在启动 HTTP 监听前执行数据库升级。迁移期间会通过同一条专用 MySQL session 持有 `stressbot_schema_migration` advisory lock；迁移或 schema post-check 失败时，Admin 关闭数据库连接并以非零状态退出，不开放管理面或控制面端口。

## 上线前防护

1. 确认当前版本和待执行版本：

   ```bash
   stressbot-admin -config /etc/stressbot/admin-config.json -migration status
   ```

2. 使用云数据库快照，或执行包含存储对象的逻辑备份：

   ```bash
   mysqldump --single-transaction --routines --triggers stressbot > stressbot-before-migration.sql
   ```

3. 在 staging 从这份备份恢复一次，并运行 `-migration up`。确认第二次运行 `-migration up` 无变更且成功。
4. 涉及大表 `ALTER TABLE` 时，先评估临时磁盘、复制延迟和元数据锁等待时间；超过维护窗口的 DDL 应拆成发布前独立作业，不放在进程自动启动路径里碰运气。
5. 确认 Supervisor 使用有限重试。示例固定 `startretries=3`，连续失败后进入 `FATAL`，避免无限重启反复尝试 DDL。

## 可用命令

- `-migration auto`：默认模式，完整前向迁移和 post-check 成功后启动 Admin。
- `-migration status`：只查看版本状态，不启动 HTTP。
- `-migration up`：执行全部待处理的前向迁移并运行 post-check，不启动 HTTP。
- `-migration up-by-one`：只执行下一条前向迁移，便于维护窗口内逐步观察，不启动 HTTP。

生产二进制不提供 `down`、`reset` 或 `redo`。MySQL DDL 通常隐式提交，不能把事务回滚当成可靠恢复方式；破坏性恢复依赖上线前备份，正常修复采用新的前向 migration。

## 迁移失败处理顺序

1. 停止自动重启，避免运维排查期间重复启动：

   ```bash
   supervisorctl stop stressbot-admin
   ```

2. 保存 Admin 日志，并执行 `-migration status`。日志和命令输出可以归档，但禁止复制带密码的 DSN。
3. 按错误类别排查：

   - 迁移锁：确认没有仍在运行的 Admin 或人工迁移进程；不要直接杀锁，先确认持有 session 的身份。
   - 权限：账号至少需要本次 migration 实际使用的 `CREATE`、`ALTER`、`INDEX`、`INSERT`、`UPDATE` 权限，以及读取 `information_schema` 的能力。
   - 容量：检查数据盘、临时表空间、binlog 和复制节点余量。
   - DDL：MySQL 已提交的前序 DDL 会保留。查看失败点，确认 migration 的可重入检查能从该状态继续。
   - post-check：Goose 版本完成但必需列、主键、唯一索引或模板名二进制排序规则不符时，Admin 仍会拒绝启动。发布前向修复 migration，不要手改版本表伪造成功。

4. 修正权限/容量/锁等环境问题，或发布新的前向修复 migration。
5. 在 staging 用同一份生产快照重放失败和恢复过程，确认：失败版本未标记完成、已提交 DDL 被识别、再次 `up` 能完成、第二次 `up` 幂等。
6. 在生产手工执行：

   ```bash
   stressbot-admin -config /etc/stressbot/admin-config.json -migration up
   stressbot-admin -config /etc/stressbot/admin-config.json -migration status
   ```

7. 只有迁移和 post-check 都成功后才恢复服务：

   ```bash
   supervisorctl start stressbot-admin
   ```

## 锁超时与并发启动

锁等待默认 30 秒。返回 0 视为超时，返回 `NULL` 视为 MySQL 错误；两种情况都关闭专用 session。迁移回调失败或业务 context 取消后，释放锁使用独立的 5 秒清理上下文，避免取消信号跳过解锁。`mysql.maxOpenConns=1` 会被拒绝，因为 advisory lock 占用一条连接，Goose 还需要另一条连接执行 migration。

## 集成演练

测试只允许对库名包含 `test` 的专用数据库执行，且会删除该库中的 stressbot 表：

```powershell
$env:STRESSBOT_TEST_MYSQL_DSN='user:password@tcp(127.0.0.1:3306)/stressbot_migration_test?parseTime=true'
$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'
go test ./admin -run TestMigrationIntegration -v -count=1
```

演练覆盖空库、早期历史表、旧模板索引、DDL 后失败再前向恢复，以及两个 Admin 并发争用迁移锁。测试和生产日志都不得输出 DSN。

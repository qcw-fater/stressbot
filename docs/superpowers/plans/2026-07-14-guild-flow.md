# Guild Flow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `guild.json` coordinate only through guilds created by online robots in the current stress-test task while preserving the existing loop and random guild behavior.

**Architecture:** Keep role selection and simple state checks declarative. Robots with `state:index % 15 == 0` create guilds; two focused Lua actions publish those guilds to task-scoped shared state and randomly select a current-task target. Existing random approval, rejection, exit, kick, and management behavior remains intact.

**Tech Stack:** stressbot JSON flow engine, Lua/gopher-lua, Redis-backed `share` module, dynamic protobuf.

---

## File structure

- Modify `conf/flow/guild.json`: graph, declarative actions/stores/listens, valid `onError`, current-task guild-pool integration.
- Create `conf/scripts/guild_publish.lua`: publish/refresh one online creator guild.
- Create `conf/scripts/guild_select_target.lua`: randomly choose one online guild from the task pool.
- Modify `conf/scripts/listen_guild_join.lua`: consume listen field table and update only the joining robot.
- Modify `conf/scripts/listen_guild_kick_member.lua`: consume listen field table and clear only the affected robot.
- Modify `conf/scripts/listen_guild_member_update.lua`: consume listen field table and update only the affected robot.
- Delete obsolete scripts only after repository-wide reference checks: `guild_drain.lua`, `listen_guild_update.lua`, `has_guild.lua`, `is_guild_manager.lua`, `is_guild_leader.lua`, `guild_join.lua`.

### Task 1: Add task-scoped guild-pool scripts

- [ ] Create `guild_publish.lua` with strict checks for fixed creator index, valid string-safe guild ID, real leader position, guild name, and `share.hash_set("guild:v3:pool", tostring(index), record, 120)`.
- [ ] Create `guild_select_target.lua` to clear target state, read the pool, filter malformed or older-than-60-second records, choose one uniformly, and store `taskGuildTargetId`, `taskGuildTargetName`, and `taskGuildTargetReady`.
- [ ] Load both scripts with gopher-lua and expect no syntax errors.

### Task 2: Rewire the guild flow declaratively

- [ ] Replace every legacy `errorStrategy` in `conf/flow/guild.json` with the equivalent `onError.strategy`.
- [ ] Change `decideGuildPath`, `judgeRole`, and `judgeLeader` to declaration expressions over `playerData.guildInfo`.
- [ ] Replace out-of-guild weighted creation with `state:index % 15 == 0`: creators run `CreateGuild → GuildPublish`, participants randomly run current-task search or join branches.
- [ ] Add `SelectTaskGuild` plus `hasTaskGuildTarget` branches before search/join.
- [ ] Make `SearchGuildList` bind `taskGuildTargetName`; make `JoinGuild` a declarative request binding `taskGuildTargetId` and storing membership only when `mydata` exists.
- [ ] Expand `GetGuildInfo.store` to refresh guild ID, base info, settings, current member data, and route data.
- [ ] Call the publish action after current guild info is refreshed; it is a no-op for non-creators/non-leaders.
- [ ] Correct new-role HeroRoot to GM type 70 and correct the level-reward description.

### Task 3: Fix real-time guild listens

- [ ] Update the three filtered listen scripts to read `msg` directly rather than call `proto.get_field_map(msg)`.
- [ ] Require `memberId == roleId` before a join push replaces local guild identity.
- [ ] Keep self-only filtering for kick/exit and member updates.
- [ ] Replace `guildUpdate` Lua listen with declarative stores for base info, settings, statistics, time record, and route data.

### Task 4: Remove obsolete paths safely

- [ ] Search all JSON, Lua, Go, and Markdown references for each obsolete runtime script.
- [ ] Remove only scripts with no remaining runtime/config references; documentation references do not keep obsolete runtime files alive.
- [ ] Confirm `guild.json` has no `GuildDrain`, legacy boolean Lua, or Lua `JoinGuild` action references.

### Task 5: Validate configuration and behavior

- [ ] Parse `guild.json` as strict JSON and verify all graph node/action/listen/script references and reachability.
- [ ] Verify `errorStrategy` count is zero and no guild ID in modified Lua is passed through `tonumber`.
- [ ] Run `go build ./...`.
- [ ] Run `cd cmd/web && npx tsc -b`.
- [ ] Run `cd cmd/web && npm run test`.
- [ ] Run Lua syntax loading for all modified scripts.
- [ ] If the configured game/Redis services are available, run the guild flow for 3–5 minutes and inspect framework/config/state errors separately from expected guild business errors.
- [ ] Do not commit or branch unless the user explicitly requests it.

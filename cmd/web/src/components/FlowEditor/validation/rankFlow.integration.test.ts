import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import type { TaskFlow } from '@/types/flow';
import { validateFlow } from './refsCheck';

const loadRankFlow = (): TaskFlow => JSON.parse(
  readFileSync(resolve(process.cwd(), '../../conf/flow/rank.json'), 'utf8'),
) as TaskFlow;

const loadRankScript = (name: string): string => readFileSync(
  resolve(process.cwd(), `../../conf/scripts/${name}`),
  'utf8',
);

describe('排位流程匹配与加载稳定性', () => {
  it('匹配成功按精确当前队伍接收推送，状态只诊断不阻塞，并共享一个总超时窗口', () => {
    const source = loadRankScript('match_succeed.lua');

    expect(source).toContain('local deadlineMs = utils.time_ms() + MATCH_TIMEOUT_MS');
    expect(source).toContain('same_id(candidate.actorTeamId, currentTeamId)');
    expect(source).toContain('if teamMatches then');
    expect(source).not.toContain('and playerStatus == MATCH_SUCCEED_STATUS');
    expect(source).not.toContain('wait_for_match_status');
    expect(source).toContain('丢弃旧 MatchSucceed');
    expect(source).not.toContain('tonumber(currentTeamId)');
    expect(source).not.toContain('tonumber(actorTeamId)');
  });

  it('进入房间同时消费成功与返回大厅信号，失败立即止损且旧消息不得串轮', () => {
    const flow = loadRankFlow();
    const matchSource = loadRankScript('match_succeed.lua');
    const source = loadRankScript('wait_match_enter_room.lua');

    expect(flow.nodes.rankedBattlePhase.next).toEqual([
      'MatchSucceed', 'ConfirmMatchSuccess', 'WaitMatchEnterRoom',
      'RoomBPSelectHero', 'RoomBPConfirmHero', 'ListenStartLoading',
      'rankedCanEnterBattle',
    ]);
    expect(flow.nodes.WaitMatchEnterRoom.onError).toEqual({ strategy: 'skip' });
    expect(flow.nodes.ConnectLogicTCP.listenRefs).toContainEqual({
      route: { cmd: 2, act: 10 },
      server: 'tcp:logic',
      listen: 'matchBackLobby',
    });
    expect(flow.listens.matchBackLobby).toEqual({});
    expect(matchSource).toContain('drain_route({cmd=3, act=5}, "MatchEnterRoom")');
    expect(matchSource).toContain('drain_route({cmd=2, act=10}, "MainBackLobby")');
    expect(source).toContain('try_parse({cmd=3, act=5}, "Game.MatchEnterRoomS2C")');
    expect(source).toContain('try_parse({cmd=2, act=10}, "Game.MainBackLobbyS2C")');
    expect(source).toContain('pcall(proto.parse, protoName, raw)');
    expect(source).toContain('return robot.error(54, "当前轮匹配已返回大厅');
    expect(source).toContain('candidate.overTime < nowSec');
    expect(source).toContain('robot.set("matchRoomId", candidate.roomId)');
    expect(flow.actions.RankedClearState.keys).toContain('matchRoomId');
    expect(source).not.toContain('返回 nil 让后续 BP');
  });

  it('开始加载只接受当前匹配会话，旧推送不得污染战斗状态', () => {
    const source = loadRankScript('listen_start_loading.lua');

    expect(source).toContain('local expectedBattleSession = robot.get("battleSession")');
    expect(source).toContain('local deadlineMs = utils.time_ms() + START_LOADING_TIMEOUT_MS');
    expect(source).toContain('same_id(candidate.battleSession, expectedBattleSession)');
    expect(source).toContain('丢弃旧 BattleStartLoading');
    expect(source).not.toContain('tonumber(expectedBattleSession)');
    expect(source).not.toContain('tonumber(candidate.battleSession)');
  });

  it('288/311 清理匹配状态，并对最终加载确认做有限重试和止损', () => {
    const flow = loadRankFlow();
    const leaveSource = loadRankScript('ranked_leave_team.lua');

    expect(leaveSource).toContain('if code == 311 or code == 288 then');
    expect(flow.nodes.BattleLoadOK.onError).toEqual({
      retry: { maxRetries: 2, retryDelayMs: 500 },
      strategy: 'skip',
    });
  });

  it('修改后的排位配置通过流程编辑器基础校验', () => {
    const report = validateFlow(loadRankFlow());

    expect(report.errors, report.errors
      .map((issue) => `${issue.code}: ${issue.message}`).join('\n')).toEqual([]);
  });
});

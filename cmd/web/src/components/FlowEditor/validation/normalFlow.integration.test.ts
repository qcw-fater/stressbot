import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import type { TaskFlow } from '@/types/flow';
import { validateFlow } from './refsCheck';

const flowPath = resolve(process.cwd(), '../../conf/flow/normal.json');
const leaveScriptPath = resolve(process.cwd(), '../../conf/scripts/normal_leave_team.lua');
const parseReconnectionScriptPath = resolve(
  process.cwd(), '../../conf/scripts/normal_parse_battle_reconnection.lua',
);
const checkReconnectionScriptPath = resolve(
  process.cwd(), '../../conf/scripts/normal_check_battle_reconnection.lua',
);

const loadNormalFlow = (): TaskFlow => JSON.parse(
  readFileSync(flowPath, 'utf8'),
) as TaskFlow;

const recoveryError = {
  handler: 'NormalRecovery',
  strategy: 'skip',
};

describe('普通模式循环配置', () => {
  it('登录后先清理跨任务遗留的服务端队伍/战斗状态，再进入普通模式循环', () => {
    const flow = loadNormalFlow();

    expect(flow.nodes.logicLogin.next).toEqual([
      'ConnectLogicTCP', 'PlayerLogin', 'RequestPlayerData',
      'NormalInitStartupRecovery', 'NormalStartupRecoveryLoop', 'ReportBattleDelayCN',
    ]);
    expect(flow.nodes.NormalStartupRecoveryLoop).toMatchObject({
      type: 'loop',
      body: 'NormalStartupRecoveryAttempt',
      loopCount: -1,
      breakCondition: 'state:normalStartupRecoveryComplete',
    });
    expect(flow.nodes.NormalStartupRecoveryAttempt.next).toEqual([
      'NormalCheckBattleReconnection', 'NormalParseBattleReconnection',
      'NormalRecoveryLeaveGate', 'NormalRecoveryWaitGate',
    ]);
    expect(flow.nodes.NormalCheckBattleReconnection.onError).toEqual({
      handler: 'NormalRecoveryWait',
      retry: { maxRetries: 1, retryDelayMs: 2000 },
      strategy: 'skip',
    });
    expect(flow.actions.NormalCheckBattleReconnection).toEqual({
      pattern: 'lua',
      script: 'normal_check_battle_reconnection.lua',
    });
  });

  it('重连检查把服务端已无队伍视为幂等成功，其余错误与超时仍向上返回', () => {
    expect(existsSync(checkReconnectionScriptPath)).toBe(true);
    if (!existsSync(checkReconnectionScriptPath)) return;

    const source = readFileSync(checkReconnectionScriptPath, 'utf8');
    expect(source).toContain('network.tcp_request("logic", {cmd=4, act=19}');
    expect(source).toContain('"Game.BattleCheckReconnectionS2C", 60');
    expect(source).toContain('if tonumber(err.code) == 310 then');
    expect(source).toContain('if type(resp) == "string" and #resp == 0 then');
    expect(source).toContain('robot.set("normalStartupRecoveryComplete", true)');
    expect(source).toContain('robot.set("normalRecoveryCanLeaveTeam", false)');
    expect(source).toContain('return err');
    expect(source).toContain('robot.set("normalBattleReconnection", resp)');
  });

  it('官方重连响应区分空响应与真实战斗，并保留服务端雪花 ID 精度', () => {
    expect(existsSync(parseReconnectionScriptPath)).toBe(true);
    if (!existsSync(parseReconnectionScriptPath)) return;

    const parseSource = readFileSync(parseReconnectionScriptPath, 'utf8');
    expect(parseSource).toContain('proto.get_path(reconnection, "teamData.teamId")');
    expect(parseSource).toContain('proto.get_path(reconnection, "roomData.roomId")');
    expect(parseSource).toContain('proto.get_field(reconnection, "battleId")');
    expect(parseSource).not.toContain('tonumber(teamId)');
    expect(parseSource).not.toContain('tonumber(roomId)');
    expect(parseSource).not.toContain('tonumber(battleId)');
    expect(parseSource).toContain('robot.set("teamId", teamId)');
    expect(parseSource).toContain('robot.set("normalStartupRecoveryComplete", not active)');
    expect(parseSource).toContain('robot.get("normalStartupRecoveryComplete") == true');
  });

  it('每轮入口和成功结算都先清理旧队伍再允许重新建队', () => {
    const flow = loadNormalFlow();

    expect(flow.nodes.normalModel.next?.slice(0, 4)).toEqual([
      'CloseBattleUDP', 'CloseBattleTCP', 'CleanupBattle', 'NormalLeaveTeam',
    ]);
    expect(flow.nodes.normalModel.next?.slice(4, 7)).toEqual([
      'RequestGameModeList', 'CreateNormalTeam', 'NormalMarkRoundActive',
    ]);
    expect(flow.nodes.startBattle.next).toEqual([
      'ConnectBattleTCP', 'ConnectBattleUDP', 'RegisterBattle', 'loadLoop',
      'normalAfterLoad',
    ]);
    expect(flow.nodes.normalSettlement.next?.slice(-5)).toEqual([
      'GameOver', 'CloseBattleUDP', 'CloseBattleTCP', 'CleanupBattle', 'NormalLeaveTeam',
    ]);
    expect(flow.nodes.NormalLeaveTeam).toEqual({
      type: 'action', action: 'NormalLeaveTeam', onError: { strategy: 'skip' },
    });
    expect(flow.actions.NormalLeaveTeam).toEqual({
      pattern: 'lua', script: 'normal_leave_team.lua',
    });
    expect(flow.actions.CloseBattleTCP).toEqual({ pattern: 'tcpClose', service: 'battle' });
    expect(flow.actions.CloseBattleUDP).toEqual({ pattern: 'udpClose', service: 'battle' });
  });

  it('建队后的失败统一清理，并用门禁阻止循环失败后的依赖动作', () => {
    const flow = loadNormalFlow();

    expect(flow.nodes.NormalRecovery).toBeDefined();
    expect(flow.nodes.normalAfterLoad).toBeDefined();
    expect(flow.nodes.normalAfterSync).toBeDefined();
    expect(flow.nodes.normalLoadedBattle).toBeDefined();
    if (!flow.nodes.NormalRecovery || !flow.nodes.normalAfterLoad
      || !flow.nodes.normalAfterSync || !flow.nodes.normalLoadedBattle) return;

    expect(flow.nodes.NormalRecovery.next).toEqual([
      'NormalMarkRoundFailed', 'NormalRuntimeRecoveryLoop',
    ]);
    expect(flow.nodes.NormalRuntimeRecoveryLoop).toMatchObject({
      type: 'loop',
      body: 'NormalRuntimeRecoveryAttempt',
      loopCount: -1,
      breakCondition: 'state:normalStartupRecoveryComplete',
    });
    expect(flow.nodes.NormalRuntimeRecoveryAttempt.next).toEqual([
      'CloseBattleUDP', 'CloseBattleTCP', 'CleanupBattle', 'CloseLogicTCP',
      'NormalRecoveryReconnectDelay', 'NormalRecoveryConnectLogic',
      'NormalRecoveryPlayerLogin', 'NormalRecoveryRequestPlayerData',
      'NormalInitStartupRecovery', 'NormalCheckBattleReconnection',
      'NormalParseBattleReconnection', 'NormalRecoveryLeaveGate',
      'NormalRecoveryWaitGate',
    ]);
    expect(flow.nodes.CloseLogicTCP).toEqual({
      type: 'action', action: 'CloseLogicTCP', delayMs: -1,
      onError: { strategy: 'resume' },
    });
    expect(flow.actions.CloseLogicTCP).toEqual({ pattern: 'tcpClose', service: 'logic' });
    expect(flow.nodes.NormalRecoveryConnectLogic.listenRefs)
      .toEqual(flow.nodes.ConnectLogicTCP.listenRefs);
    for (const nodeName of [
      'NormalRecoveryConnectLogic', 'NormalRecoveryPlayerLogin',
      'NormalRecoveryRequestPlayerData',
    ]) {
      expect(flow.nodes[nodeName].onError, nodeName).toEqual({
        handler: 'NormalRecoveryWait', strategy: 'skip',
      });
    }
    expect(flow.nodes.normalAfterLoad).toMatchObject({
      type: 'boolean',
      condition: 'state:normalRoundActive',
      trueNext: 'normalLoadedBattle',
    });
    expect(flow.nodes.normalAfterSync).toMatchObject({
      type: 'boolean',
      condition: 'state:normalRoundActive',
      trueNext: 'normalSettlement',
    });
    expect(flow.nodes.normalLoadedBattle.next).toEqual([
      'BattleLoadOK', 'ResetLoadState', 'StartGame', 'syncLoop', 'normalAfterSync',
    ]);

    [
      'CreateNormalTeam', 'SelectHero', 'StartMatch', 'MatchSucceed',
      'ListenStartLoading', 'ConnectBattleTCP', 'ConnectBattleUDP', 'RegisterBattle',
      'LoadProgress', 'BattleLoadOK', 'StartGame', 'SyncFrameData', 'BattleEnd',
      'BattleReward', 'GameOver',
    ].forEach((nodeName) => {
      expect(flow.nodes[nodeName].onError, nodeName).toEqual(recoveryError);
    });
  });

  it('退队脚本保留雪花 ID 精度，并在匹配中先取消匹配再重试', () => {
    const exists = existsSync(leaveScriptPath);
    expect(exists).toBe(true);
    if (!exists) return;

    const source = readFileSync(leaveScriptPath, 'utf8');
    expect(source).not.toContain('tonumber(teamId)');
    expect(source).toContain('network.tcp_request("logic", {cmd=5, act=2}');
    expect(source).toContain('network.tcp_request("logic", {cmd=5, act=21}');
    expect(source).toContain('if code == 311 or code == 288 then');
    expect(source).toContain('if code == 308 then');
    expect(source).toContain('return err2');
    expect(source).toContain('return err');
  });

  it('只注册 normal 实际消费的持久监听，并通过流程编辑器校验', () => {
    const flow = loadNormalFlow();
    const logicListens = flow.nodes.ConnectLogicTCP.listenRefs?.map((ref) => ref.listen) ?? [];

    expect(logicListens).toEqual([
      'matchPoll', 'teamStartMatch', 'loadingPoll', 'rewardPoll',
      'stateUpdate', 'shopDataUpdate', 'shopLimitDataUpdate',
    ]);
    expect(Object.keys(flow.listens).sort()).toEqual([
      'battleStartGame', 'frameData', 'loadingPoll', 'matchPoll', 'rewardPoll',
      'shopDataUpdate', 'shopLimitDataUpdate', 'stateUpdate', 'teamStartMatch',
    ]);

    const report = validateFlow(flow);
    expect(report.errors, report.errors.map((issue) => `${issue.code}: ${issue.message}`).join('\n'))
      .toEqual([]);
  });
});

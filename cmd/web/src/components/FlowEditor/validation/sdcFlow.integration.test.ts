import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import type { ActionDef } from '@/types/action';
import type { TaskFlow } from '@/types/flow';
import { collectStateKeys, collectUsedScriptNames } from '../editors/ActionEditor/stateRegistry';
import { validateFlow } from './refsCheck';

const loadSdcFlow = (): TaskFlow => JSON.parse(
  readFileSync(resolve(process.cwd(), '../../conf/flow/sdc.json'), 'utf8'),
) as TaskFlow;

const loadSdcScript = (name: string): string => readFileSync(
  resolve(process.cwd(), `../../conf/scripts/${name}`),
  'utf8',
);

const loadBattleProto = (): string => readFileSync(
  resolve(process.cwd(), '../../conf/proto/generate_battle.proto'),
  'utf8',
);

const loadAdapter = (name: string): Record<string, unknown> => JSON.parse(
  readFileSync(resolve(process.cwd(), `../../conf/adapter/${name}`), 'utf8'),
) as Record<string, unknown>;

const expectRoute = (action: ActionDef, pattern: string, cmd: number, act: number): void => {
  expect(action).toMatchObject({ pattern, route: { cmd, act } });
};

describe('SDC 单排循环配置', () => {
  it('稳定重复登录后单排战斗与局外整理循环', () => {
    const flow = loadSdcFlow();

    expect(flow.nodes.sdcSoloSetup.next).toEqual([
      'SdcManagePrepare', 'SdcGetPrepareData', 'SelectHero', 'SdcCreateTeam', 'SdcTeamPrepare',
      'SdcMarkSetupReady',
    ]);
    expect(flow.nodes.SdcOutBattlePhase.next).toEqual([
      'SdcRefreshOutBattlePrepare', 'SdcVerifyOutcomeAssets', 'SdcOutBattleOutcomeBranch',
    ]);
    expect(flow.nodes.SdcSuccessOutBattlePhase.next).toEqual([
      'SdcSellRandomOutBattleItem', 'SdcQuickDepositOutBattle', 'SdcVerifyOutBattleAssets',
    ]);
    expect(flow.nodes.sdcModel.next).toEqual([
      'SdcRecoveryGate', 'SdcRoundInit', 'SdcLeaveTeam', 'SdcCheckTeamCleared',
      'SdcTeamClearedGate', 'sdcRoundCooldown',
    ]);
    expect(flow.nodes.SdcRoundExecution.next).toEqual([
      'sdcSoloSetup', 'sdcAfterSetup', 'SdcTeamCleanup',
    ]);
  });

  it('GameOver 成功确认服务端回大厅后直接同步清理本地 teamId', () => {
    const flow = loadSdcFlow();

    expect(flow.nodes.SdcReturnedLobby.next).toEqual([
      'SdcMarkReturnedLobby', 'SdcClearTeamState', 'SdcOutBattlePhase',
    ]);
    expect(flow.nodes.SdcReturnedLobbyGate).toBeUndefined();
    expect(flow.nodes.SdcTeamCleanup.next).toEqual([
      'CloseBattleUDP', 'CloseBattleTCP', 'SdcLeaveTeam', 'SdcCheckTeamCleared',
      'SdcMarkRecoveryIfTeamRemains', 'SdcClearState',
    ]);
    expect(flow.nodes.SdcMarkRecoveryIfTeamRemains).toMatchObject({
      type: 'boolean',
      condition: 'state:sdcTeamCleared',
      falseNext: 'SdcMarkRecoveryNeeded',
    });
  });

  it('登录和每轮入口都以服务端战斗状态与退队结果作为硬门禁', () => {
    const flow = loadSdcFlow();

    expect(flow.nodes.logicLogin.next).toEqual([
      'ConnectLogicTCP', 'PlayerLogin', 'RequestPlayerData', 'SdcInitRecoveryFlags',
      'SdcReadBattleReconnection', 'SdcInitialRecoveryDecision', 'ReportBattleDelay',
    ]);
    expect(flow.nodes.SdcInitialRecoveryDecision).toMatchObject({
      type: 'boolean',
      condition: 'state:sdcLoginBattleReconnection',
      trueNext: 'SdcMarkRecoveryNeeded',
    });
    expect(flow.nodes.SdcTeamClearedGate).toMatchObject({
      type: 'boolean',
      condition: 'state:sdcTeamCleared',
      trueNext: 'SdcRoundExecution',
      falseNext: 'SdcMarkRecoveryNeeded',
    });
    expect(flow.nodes.SdcCreateTeam.onError).toEqual({
      handler: 'SdcMarkRecoveryNeeded', strategy: 'skip',
    });
  });

  it('异常恢复必须重新登录并等到服务端明确确认无需战斗重连', () => {
    const flow = loadSdcFlow();

    expect(flow.nodes.SdcRecoveryGate).toMatchObject({
      type: 'boolean',
      condition: 'state:sdcRecoveryNeeded',
      trueNext: 'SdcRecoveryLoop',
    });
    expect(flow.nodes.SdcRecoveryLoop).toMatchObject({
      type: 'loop',
      body: 'SdcRecoveryAttempt',
      loopCount: -1,
      breakCondition: 'state:sdcRecoveryComplete',
    });
    expect(flow.nodes.SdcRecoveryAttempt.next).toEqual([
      'CloseBattleUDP', 'CloseBattleTCP', 'CloseLogicTCP', 'SdcRecoveryReconnectDelay',
      'SdcRecoveryConnectLogic', 'SdcRecoveryPlayerLogin', 'SdcRecoveryRequestPlayerData',
      'SdcReadBattleReconnection', 'SdcRecoveryDecision',
    ]);
    expect(flow.nodes.SdcRecoveryDecision).toMatchObject({
      type: 'boolean',
      condition: 'state:sdcLoginBattleReconnection',
      trueNext: 'SdcRecoveryPendingWait',
      falseNext: 'SdcRecoveryLobbyCleanup',
    });
    expect(flow.nodes.SdcRecoveryLobbyCleanup.next).toEqual([
      'SdcLeaveTeam', 'SdcCheckTeamCleared', 'SdcRecoveryTeamClearedGate',
    ]);
    expect(flow.nodes.SdcRecoveryTeamClearedGate).toMatchObject({
      type: 'boolean',
      condition: 'state:sdcTeamCleared',
      trueNext: 'SdcCompleteRecovery',
      falseNext: 'SdcRecoveryPendingWait',
    });
    [
      'SdcRecoveryConnectLogic', 'SdcRecoveryPlayerLogin', 'SdcRecoveryRequestPlayerData',
    ].forEach((node) => {
      expect(flow.nodes[node].onError).toEqual({
        handler: 'SdcRecoveryPendingWait', strategy: 'skip',
      });
    });
    expect(flow.nodes.SdcCompleteRecovery.next).toEqual([
      'SdcClearTeamState', 'SdcMarkRecoveryComplete',
    ]);
    expect(flow.actions.CloseLogicTCP).toEqual({ pattern: 'tcpClose', service: 'logic' });
    expect(flow.actions.SdcClearTeamState).toMatchObject({
      pattern: 'clearState',
      keys: ['teamId'],
    });
    expect(flow.actions.SdcReadBattleReconnection).toMatchObject({
      pattern: 'lua', script: 'sdc_read_battle_reconnection.lua',
    });
    expect(flow.actions.SdcCheckTeamCleared).toMatchObject({
      pattern: 'lua', script: 'sdc_check_team_cleared.lua',
    });
    const source = loadSdcScript('sdc_read_battle_reconnection.lua');
    expect(source).not.toMatch(/require\(["']network["']\)/);
    expect(source).not.toMatch(/network\.(tcp|udp)_/);
    const teamSource = loadSdcScript('sdc_check_team_cleared.lua');
    expect(teamSource).not.toMatch(/require\(["']network["']\)/);
    expect(teamSource).not.toMatch(/network\.(tcp|udp)_/);
  });

  it('普通 SDC RPC 与奖励监听全部由声明式动作收发', () => {
    const { actions } = loadSdcFlow();

    expectRoute(actions.SdcRequestPrepareData, 'tcpRequest', 45, 1);
    expectRoute(actions.SdcRequestWarehouseData, 'tcpRequest', 45, 21);
    expectRoute(actions.SdcRequestContainerData, 'tcpRequest', 45, 12);
    expectRoute(actions.SdcRequestSuitPresetData, 'tcpRequest', 45, 121);
    expectRoute(actions.SdcStartMatchRequest, 'tcpRequest', 5, 20);
    expectRoute(actions.SdcBatchSellRequest, 'tcpRequest', 45, 6);
    expectRoute(actions.SdcQuickDepositRequest, 'tcpRequest', 45, 11);
    expectRoute(actions.SdcListenBattleReward, 'tcpListen', 4, 72);
    expect(actions.SdcListenBattleReward.timeout).toBe(90);

    expect(actions.SdcGetPrepareData).toBeUndefined();
    expect(actions.SdcStartMatch).toBeUndefined();
    expect(actions.BattleReward).toBeUndefined();
  });

  it('战备管理在流程图选择分支，十一种普通操作均为声明式请求', () => {
    const flow = loadSdcFlow();
    const routes: Record<string, number> = {
      SdcEquipItemRequest: 2,
      SdcUnequipItemRequest: 3,
      SdcMoveItemRequest: 4,
      SdcPackUseRequest: 9,
      SdcSwapItemRequest: 10,
      SdcActivateContainerRequest: 13,
      SdcSwitchContainerRequest: 14,
      SdcMoveContainerAllRequest: 15,
      SdcUseSuitRequest: 123,
      SdcCancelSuitRequest: 124,
      SdcUsePresetRequest: 127,
    };

    expect(flow.nodes.SdcManagePrepare).toMatchObject({ type: 'sequence' });
    expect(flow.nodes.SdcPrepareActionSwitch).toMatchObject({ type: 'switch' });
    Object.entries(routes).forEach(([name, act]) => {
      expectRoute(flow.actions[name], 'tcpRequest', 45, act);
    });

    expect(flow.actions.SdcSelectPrepareAction).toMatchObject({
      pattern: 'lua', script: 'sdc_select_prepare_action.lua',
    });
    expect(loadSdcScript('sdc_select_prepare_action.lua')).not.toMatch(/require\(["']network["']\)/);
    expect(loadSdcScript('sdc_select_prepare_action.lua')).not.toContain('network.tcp_request');
    expect(flow.actions.SdcSavePreset).toMatchObject({
      pattern: 'lua', script: 'sdc_save_prepare_preset.lua',
    });
    expect(loadSdcScript('sdc_save_prepare_preset.lua'))
      .not.toContain('proto.get_path(resp, "errorCode")');
  });

  it('局外刷新、出售、入库和资产校验使用声明式 I/O 与独立 Lua 校验器', () => {
    const flow = loadSdcFlow();

    expect(flow.nodes.SdcRefreshOutBattlePrepare).toMatchObject({ type: 'sequence' });
    expect(flow.nodes.SdcSellRandomOutBattleItem).toMatchObject({ type: 'sequence' });
    expect(flow.nodes.SdcQuickDepositOutBattle).toMatchObject({ type: 'sequence' });
    expect(flow.nodes.SdcVerifyOutcomeAssets).toMatchObject({ type: 'sequence' });
    expect(flow.nodes.SdcVerifyOutBattleAssets).toMatchObject({ type: 'sequence' });

    [
      'sdc_summarize_out_battle_prepare.lua',
      'sdc_validate_sell_result.lua',
      'sdc_validate_quick_deposit.lua',
      'sdc_verify_outcome_assets.lua',
      'sdc_verify_out_battle_assets.lua',
    ].forEach((script) => {
      expect(loadSdcScript(script)).not.toMatch(/require\(["']network["']\)/);
      expect(loadSdcScript(script)).not.toContain('network.tcp_request');
    });
  });

  it('局内初始化与开箱拆成单一职责节点且均不负责网络收发', () => {
    const flow = loadSdcFlow();

    expect(flow.nodes.sdcBattleTick.next).toEqual([
      'SdcSyncFrame', 'SdcFrameInterval', 'SdcUpdateDwellState', 'SdcLootEvent',
    ]);
    expect(loadSdcScript('sdc_sync_frame.lua')).not.toContain('sdcDwellDone');
    expect(loadSdcScript('sdc_sync_frame.lua')).not.toContain('utils.sleep');
    expect(flow.nodes.SdcFrameInterval).toEqual({
      type: 'weighted',
      options: [
        { node: 'SdcFrameWait5s', weight: 1 },
        { node: 'SdcFrameWait5500ms', weight: 1 },
        { node: 'SdcFrameWait6s', weight: 1 },
      ],
    });
    expect(flow.nodes.SdcFrameWait5s).toEqual({ type: 'wait', waitMs: 5000 });
    expect(flow.nodes.SdcFrameWait5500ms).toEqual({ type: 'wait', waitMs: 5500 });
    expect(flow.nodes.SdcFrameWait6s).toEqual({ type: 'wait', waitMs: 6000 });
    expect(flow.nodes.SdcFinalSyncFrame).toEqual({
      type: 'action', action: 'SdcSyncFrame', delayMs: -1,
    });
    const startBattleNext = flow.nodes.startBattle.next ?? [];
    expect(startBattleNext).toContain('SdcFinalSyncFrame');
    expect(startBattleNext.indexOf('SdcFinalSyncFrame'))
      .toBeLessThan(startBattleNext.indexOf('CloseBattleUDP'));
    expect(loadSdcScript('sdc_update_dwell_state.lua')).not.toMatch(/require\(["']network["']\)/);
    expect(flow.nodes.SdcInitBattle.next).toEqual([
      'SdcInitBattleState', 'SdcInitBattleTiming', 'SdcInitBattleInventory',
    ]);
    expect(flow.nodes.SdcLootEvent.next).toEqual([
      'SdcSelectChest', 'SdcHasSelectedChest',
    ]);
    expect(flow.nodes.SdcOpenSelectedChest.next).toEqual([
      'SdcConsumeChestKey', 'SdcCollectChestLoot', 'SdcCompleteChestOpen',
    ]);

    [
      'sdc_init_battle_timing.lua',
      'sdc_init_battle_inventory.lua',
      'sdc_select_chest.lua',
      'sdc_consume_chest_key.lua',
      'sdc_collect_chest_loot.lua',
      'sdc_complete_chest_open.lua',
    ].forEach((script) => {
      const source = loadSdcScript(script);
      expect(source).not.toMatch(/require\(["']network["']\)/);
      expect(source).not.toMatch(/network\.(tcp|udp)_/);
    });

    const collectSource = loadSdcScript('sdc_collect_chest_loot.lua');
    expect(collectSource).toMatch(/require\(["']share["']\)/);
    expect(collectSource).toContain('share.claim');
    expect(collectSource).toContain('sdc:loot:');
    expect(collectSource).toContain('tostring(battleId)');
    expect(collectSource).toContain('tostring(guid)');
  });

  it('结局分类、结算提交与奖励校验职责分离', () => {
    const flow = loadSdcFlow();

    expect(flow.nodes.SdcOutcomeBranch).toMatchObject({
      type: 'weighted',
      options: [
        { node: 'SdcOutcomeSuccess', weight: 60 },
        { node: 'SdcOutcomeEvacuationFailed', weight: 20 },
        { node: 'SdcOutcomeDeath', weight: 20 },
      ],
    });
    expect(flow.nodes.SdcBattleEnd.next).toEqual([
      'SdcClassifySettlement', 'SdcSendSettlement', 'SdcMarkSettlementConfirmed',
    ]);
    expect(flow.nodes.SdcAfterSettlement).toMatchObject({
      type: 'boolean',
      condition: 'state:sdcSettlementConfirmed == 1',
      trueNext: 'SdcSettlementConfirmedPath',
      falseNext: 'SdcBattleRequestFailureRecovery',
    });
    expect(flow.nodes.SdcSettlementConfirmedPath.next).toEqual([
      'SdcExitBattle', 'CloseBattleTCP', 'BattleReward',
    ]);
    expect(flow.nodes.SdcBattleRequestFailureRecovery.next).toEqual([
      'CloseBattleTCP', 'SdcMarkRecoveryNeeded',
    ]);
    expect(flow.nodes.SdcWaitBattleRecovery).toBeUndefined();
    expect(flow.nodes.SdcExitBattle.onError).toEqual({
      handler: 'SdcBattleRequestFailureRecovery', strategy: 'skip',
    });
    expectRoute(flow.actions.SdcExitBattle, 'tcpRequest', 4, 87);
    expect(flow.actions.SdcExitBattle).toMatchObject({
      c2sProto: 'Game.BattleMidwaySettlementExitC2S',
      s2cProto: 'Game.BattleMidwaySettlementExitS2C',
      timeout: 90,
    });
    expect(loadBattleProto()).toContain('int32        settlementFighterIndex = 4;');
    expect(flow.actions.SdcRequestPlayerExit).toBeUndefined();
    const settlementScript = loadSdcScript('sdc_send_settlement.lua');
    expect(settlementScript)
      .toContain('local serverIndex = tonumber(fighter.serverFightIndex)');
    expect(settlementScript)
      .toContain('proto.set_field(msg, "settlementFighterIndex", selfIndex)');
    expect(settlementScript)
      .toContain('proto.set_field(battleEnd, "playerResult", stats)');
    expect(settlementScript)
      .toContain('"Game.BattleMidwaySettlementS2C", 90)');
    expect(settlementScript)
      .not.toContain('proto.set_field(battleEnd, "playerResult", {stat})');
    expect(flow.nodes.BattleReward.next).toEqual([
      'SdcListenBattleReward', 'SdcValidateRewardIdentity', 'SdcValidateRewardOutcome',
      'GameOver', 'SdcReturnedLobby',
    ]);
    expect(flow.nodes.SdcListenBattleReward.onError).toEqual({
      handler: 'SdcRewardFailureRecovery', strategy: 'skip',
    });
    expect(flow.nodes.SdcRewardFailureRecovery.next).toEqual([
      'SdcLogRewardTimeout', 'SdcMarkRecoveryNeeded',
    ]);
    expect(flow.nodes.GameOver.onError).toEqual({
      handler: 'SdcMarkRecoveryNeeded', strategy: 'skip',
    });
    expect(flow.actions.SdcLogRewardTimeout).toMatchObject({
      pattern: 'lua', script: 'sdc_log_reward_timeout.lua',
    });
    expect(loadSdcScript('sdc_log_reward_timeout.lua')).not.toMatch(/require\(["']network["']\)/);
    expect(loadSdcScript('sdc_classify_settlement.lua')).not.toMatch(/require\(["']network["']\)/);
    expect(loadSdcScript('sdc_classify_settlement.lua')).not.toContain('proto.create');
    expect(loadSdcScript('sdc_validate_reward_identity.lua')).not.toMatch(/require\(["']network["']\)/);
    expect(loadSdcScript('sdc_validate_reward_outcome.lua')).not.toMatch(/require\(["']network["']\)/);
    expect(loadSdcScript('sdc_validate_reward_outcome.lua')).not.toContain('battleLootValue > 0');
  });

  it('隔离共享入局脚本并通过流程编辑器校验', () => {
    const flow = loadSdcFlow();

    expect(flow.actions.ListenStartLoading).toMatchObject({
      pattern: 'lua', script: 'listen_sdc_start_loading.lua',
    });
    const sdcUdpCodec = loadAdapter('udp_sdc_battle_codec.json') as {
      heartbeat?: { intervalMs?: number };
    };
    const sharedUdpCodec = loadAdapter('udp_battle_codec.json') as {
      heartbeat?: { intervalMs?: number };
    };
    expect(flow.nodes.ConnectBattleUDP.listenRefs).toBeUndefined();
    expect(flow.listens.frameData).toBeUndefined();
    expect(flow.actions.ConnectBattleUDP).toMatchObject({
      pattern: 'lua', script: 'connect_sdc_battle_udp.lua',
    });
    expect(flow.actions.CloseBattleUDP).toEqual({ pattern: 'udpClose', service: 'sdc_battle' });
    expect(flow.nodes.SdcBattleDrainWait).toBeUndefined();
    expect(sdcUdpCodec.heartbeat?.intervalMs).toBe(5000);
    expect(sharedUdpCodec.heartbeat?.intervalMs).toBe(150);
    const connectUdpSource = loadSdcScript('connect_sdc_battle_udp.lua');
    expect(connectUdpSource).toContain('local SERVICE = "sdc_battle"');
    expect(connectUdpSource).toContain('network.connect_udp(SERVICE');
    expect(connectUdpSource).toContain('network.set_udp_secret_key(SERVICE');
    const frameSource = loadSdcScript('sdc_sync_frame.lua');
    expect(frameSource).not.toContain('network.try_udp_listen');
    expect(frameSource).toContain('network.udp_send("sdc_battle"');
    const loadingScript = loadSdcScript('listen_sdc_start_loading.lua');
    expect(loadingScript).toContain('serverFightIndex = i - 1');
    expect(loadingScript)
      .toContain('teamId = proto.get_path(fighter, "matchData.teamId") or 0');
    expect(loadingScript)
      .not.toContain('teamId = tonumber(proto.get_path(fighter, "matchData.teamId"))');
    expect(loadSdcScript('listen_start_loading.lua')).not.toMatch(/sdc/i);
    expect(flow.nodes.startBattle.next).toEqual([
      'ConnectBattleTCP', 'ConnectBattleUDP', 'RegisterBattle', 'loadLoop', 'BattleLoadOK',
      'ResetLoadState', 'StartGame', 'SdcInitBattle', 'sdcSyncLoop', 'SdcFinalSyncFrame',
      'SdcOutcomeBranch', 'SdcBattleEnd', 'CloseBattleUDP', 'SdcAfterSettlement',
    ]);
    const scriptNames = collectUsedScriptNames(flow.actions, flow.listens, flow.nodes);
    const scripts = [...scriptNames].map((name) => ({ name, content: loadSdcScript(name) }));
    const stateKeys = collectStateKeys(flow.actions, flow.listens, undefined, undefined, scripts);
    const report = validateFlow(flow, { stateKeys, stateKeysReady: true });

    expect(report.errors).toEqual([]);
    expect(report.warnings.filter((issue) => issue.code === 'CLEARSTATE_UNKNOWN_KEY')).toEqual([]);
  });
});

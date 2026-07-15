/**
 * 流程文件的导入/导出逻辑（供顶部「文件」菜单与「流程管理」弹窗共用，单一来源）。
 *
 * - importFlow：读本地 JSON → 解析为 FlowJson → loadFromTaskFlow → 同步引用脚本；
 *   返回是否成功，调用方据此决定是否关闭弹窗。
 * - exportFlow：当前草稿序列化为 flow.json 下载。
 * - syncScriptsAfterLoad：加载/导入流程后对引用的 lua 脚本做 gap-fill，供「加载基线流程」复用。
 */

import { App as AntApp } from 'antd';
import { useFlowStore } from '../store/flowStore';
import { syncFlowScriptsToIdb } from '@/services/scriptSync';
import type { FlowJson } from '../codec/flowToJson';

export function useFlowFileIO() {
  const { message } = AntApp.useApp();
  const loadFromTaskFlow = useFlowStore((s) => s.loadFromTaskFlow);

  /**
   * 加载/导入 flow 后自动把引用的 lua 脚本同步到本地存储。
   * - 静默 skipped（本地已存在），只对 missing 给提示；
   * - missing 用 warning（不阻塞，用户也许稍后会手敲）；
   * - 任何异常都吞掉，不影响加载主流程。
   * 与服务器的全量对比 / 冲突合并已改为显式操作（资源管理面板的「拉取」按钮），
   * 不再在加载/导入流程时自动触发，避免无感覆盖用户的本地编辑稿。
   */
  const syncScriptsAfterLoad = async (flow: FlowJson) => {
    try {
      const { missing } = await syncFlowScriptsToIdb(flow);
      if (missing.length > 0) {
        message.warning(
          `${missing.length} 个被引用的 lua 脚本不存在于 conf/scripts/，` +
            `启动任务前请到「资源管理」上传或在动作里手写：${missing.join(', ')}`,
          8,
        );
      }
    } catch {
      // 同步失败不阻塞主流程
    }
  };

  /** 导入本地流程 JSON。成功返回 true。 */
  const importFlow = async (file: File): Promise<boolean> => {
    try {
      const text = await file.text();
      const parsed = JSON.parse(text) as FlowJson;
      loadFromTaskFlow(parsed);
      message.success(`已加载 ${file.name}`);
      await syncScriptsAfterLoad(parsed);
      return true;
    } catch (e) {
      message.error(`导入失败：${(e as Error).message}`);
      return false;
    }
  };

  /** 导出当前草稿为 flow.json 下载。 */
  const exportFlow = () => {
    const flow = useFlowStore.getState().toTaskFlow();
    const blob = new Blob([JSON.stringify(flow, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'flow.json';
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  };

  return { importFlow, exportFlow, syncScriptsAfterLoad };
}

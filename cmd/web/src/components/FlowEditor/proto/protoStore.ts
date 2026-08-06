/**
 * proto 加载状态 store。
 */

import { create } from 'zustand';
import { protoRegistry } from './ProtoRegistry';
import { loadProtos, type ProtoSource } from './ProtoLoader';

interface ProtoState {
  status: 'idle' | 'loading' | 'ready' | 'error';
  error?: string;
  hash?: string;
  fileCount: number;
  /**
   * 本次加载的编译期错误（语法错误、缺失 import、未知类型等）。
   * status='ready' 时仍可能非空——注册表是「部分可用」的（至少有 1 个 message 注册成功）。
   * 暴露出来让上层（校验/UI）可知注册表是否完整，避免拿着残缺注册表盲目判定 proto 缺失。
   */
  errors: string[];
  /** 上一次加载使用的源；reload 时复用 */
  lastSource?: ProtoSource;
  load: (source: ProtoSource) => Promise<void>;
  /** 用上次的 source 重新加载；首次未加载时无效 */
  reload: () => Promise<void>;
}

export const useProtoStore = create<ProtoState>()((set, get) => ({
  status: 'idle',
  fileCount: 0,
  errors: [],
  load: async (source) => {
    console.log(`[ProtoStore] 开始加载 proto（source=${source.kind}）`);
    set({ status: 'loading', error: undefined, errors: [], lastSource: source });
    try {
      const result = await loadProtos(source);
      protoRegistry.load(result.root);
      set({ status: 'ready', hash: result.hash, fileCount: result.files.length, errors: result.errors });
      console.log(`[ProtoStore] 加载成功，状态切换为 ready（${result.files.length} 文件）`);
    } catch (e) {
      const msg = (e as Error).message;
      console.error(`[ProtoStore] 加载失败：${msg}`);
      set({ status: 'error', error: msg, errors: [] });
    }
  },
  reload: async () => {
    const last = get().lastSource;
    if (!last) {
      console.warn('[ProtoStore] reload 跳过：尚未首次加载');
      return;
    }
    return get().load(last);
  },
}));

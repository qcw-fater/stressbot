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
  /** 上一次加载使用的源；reload 时复用 */
  lastSource?: ProtoSource;
  load: (source: ProtoSource) => Promise<void>;
  /** 用上次的 source 重新加载；首次未加载时无效 */
  reload: () => Promise<void>;
}

export const useProtoStore = create<ProtoState>((set, get) => ({
  status: 'idle',
  fileCount: 0,
  load: async (source) => {
    console.log(`[ProtoStore] 开始加载 proto（source=${source.kind}）`);
    set({ status: 'loading', error: undefined, lastSource: source });
    try {
      const result = await loadProtos(source);
      protoRegistry.load(result.root);
      set({ status: 'ready', hash: result.hash, fileCount: result.files.length });
      console.log(`[ProtoStore] 加载成功，状态切换为 ready（${result.files.length} 文件）`);
    } catch (e) {
      const msg = (e as Error).message;
      console.error(`[ProtoStore] 加载失败：${msg}`);
      set({ status: 'error', error: msg });
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

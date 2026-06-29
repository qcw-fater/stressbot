import { useEffect, useState } from 'react';
import { listCodecConnections, loadCodecRouteSpecs, type CodecConnection, type CodecRouteSpec } from '@/services/codecConnections';
import { subscribe } from '@/services/resourcesStore';

interface CodecConnectionsState {
  loading: boolean;
  error: string | null;
  connections: CodecConnection[];
}

interface CodecRouteSpecsState {
  loading: boolean;
  error: string | null;
  specs: Map<string, CodecRouteSpec>;
}

export function useCodecConnections(): CodecConnectionsState {
  const [state, setState] = useState<CodecConnectionsState>({ loading: true, error: null, connections: [] });

  useEffect(() => {
    let alive = true;
    const load = () => {
      setState((s) => ({ ...s, loading: true, error: null }));
      void listCodecConnections()
        .then((connections) => {
          if (alive) setState({ loading: false, error: null, connections });
        })
        .catch((e) => {
          if (alive) setState({ loading: false, error: (e as Error).message, connections: [] });
        });
    };
    load();
    const unsub = subscribe(load);
    return () => {
      alive = false;
      unsub();
    };
  }, []);

  return state;
}

export function useCodecRouteSpecs(): CodecRouteSpecsState {
  const [state, setState] = useState<CodecRouteSpecsState>({ loading: true, error: null, specs: new Map() });

  useEffect(() => {
    let alive = true;
    const load = () => {
      setState((s) => ({ ...s, loading: true, error: null }));
      void loadCodecRouteSpecs()
        .then((specs) => {
          if (alive) setState({ loading: false, error: null, specs });
        })
        .catch((e) => {
          if (alive) setState({ loading: false, error: (e as Error).message, specs: new Map() });
        });
    };
    load();
    const unsub = subscribe(load);
    return () => {
      alive = false;
      unsub();
    };
  }, []);

  return state;
}

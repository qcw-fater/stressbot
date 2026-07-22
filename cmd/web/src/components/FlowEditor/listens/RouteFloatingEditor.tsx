import { createPortal } from 'react-dom';
import { FloatingWindow } from '../panels/FloatingWindow';
import { RouteEditor } from './RouteEditor';

export interface RouteFloatingEditorProps {
  windowId: string;
  open: boolean;
  value: unknown;
  onChange: (value: unknown) => void;
  onClose: () => void;
  server?: string;
  routeKeyTemplate?: string;
  loading?: boolean;
  error?: string | null;
}

const DEFAULT_SIZE = { width: 560, height: 112 };
const MIN_SIZE = { width: 360, height: 96 };

function getDefaultPosition() {
  if (typeof window === 'undefined') return { x: 0, y: 0 };
  return {
    x: Math.max(0, (window.innerWidth - DEFAULT_SIZE.width) / 2),
    y: Math.max(0, (window.innerHeight - DEFAULT_SIZE.height) / 2),
  };
}

export function RouteFloatingEditor({
  windowId,
  open,
  value,
  onChange,
  onClose,
  server,
  routeKeyTemplate,
  loading,
  error,
}: RouteFloatingEditorProps) {
  if (typeof document === 'undefined') return null;

  return createPortal(
    <FloatingWindow
      windowId={windowId}
      title="编辑 route"
      defaultSize={DEFAULT_SIZE}
      defaultPosition={getDefaultPosition()}
      minSize={MIN_SIZE}
      open={open}
      onClose={onClose}
    >
      <div className="route-floating-editor">
        <RouteEditor
          layout="inline"
          size="small"
          server={server}
          value={value}
          routeKeyTemplate={routeKeyTemplate}
          loading={loading}
          error={error}
          onChange={onChange}
        />
      </div>
    </FloatingWindow>,
    document.body,
  );
}

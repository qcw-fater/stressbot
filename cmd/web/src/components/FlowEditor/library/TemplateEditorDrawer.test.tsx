import { App as AntApp } from 'antd';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { TemplateEditorDrawer } from './TemplateEditorDrawer';

const mocks = vi.hoisted(() => ({
  capability: {
    templateLibrary: true as boolean | undefined,
    loading: false,
    error: undefined as Error | undefined,
    refresh: vi.fn(),
  },
  closePanel: vi.fn(),
  getAction: vi.fn(),
  updateAction: vi.fn(),
  showApiError: vi.fn(),
}));

vi.mock('./useTemplateLibraryCapability', () => ({
  useTemplateLibraryCapability: () => mocks.capability,
}));
vi.mock('@/services/errorHandler', () => ({ showApiError: mocks.showApiError }));
vi.mock('../store/editorStore', () => ({
  useEditorStore: (selector: (state: unknown) => unknown) =>
    selector({
      activePanel: {
        templateEdit: { kind: 'templateEdit', templateKind: 'action', templateId: 'a1' },
      },
      closePanel: mocks.closePanel,
    }),
}));
vi.mock('./templateStore', () => ({
  getActionTemplate: mocks.getAction,
  getListenTemplate: vi.fn(),
  updateActionTemplate: mocks.updateAction,
  updateListenTemplate: vi.fn(),
}));
vi.mock('../panels/FloatingWindow', () => ({
  FloatingWindow: ({
    open,
    title,
    footer,
    children,
  }: {
    open: boolean;
    title: React.ReactNode;
    footer: React.ReactNode;
    children: React.ReactNode;
  }) =>
    open ? (
      <div>
        {title}
        {children}
        {footer}
      </div>
    ) : null,
}));
// 桩件暴露一个按钮来触发 pattern 切换：真的去点 antd Select 要摸它的内部类名，
// 而这里要验的只是「切换后 def 怎么写回」，与下拉自身的渲染无关。
vi.mock('../editors/ActionEditor/PatternSelector', () => ({
  PatternSelector: ({ onChange }: { onChange: (p: string) => void }) => (
    <button onClick={() => onChange('clearState')}>切到 clearState</button>
  ),
}));
vi.mock('../editors/ActionEditor/DeclarativeForm', () => ({
  DeclarativeForm: () => <div>动作内容</div>,
}));
vi.mock('../editors/ActionEditor/LuaForm', () => ({ LuaForm: () => null }));
vi.mock('../editors/ActionEditor/StoreTable', () => ({ StoreTable: () => null }));
vi.mock('../proto/ProtoBrowser', () => ({ ProtoBrowser: () => null }));
vi.mock('../codec/TargetConnectionRouteEditor', () => ({
  TargetConnectionRouteEditor: () => null,
}));
vi.mock('../codec/useCodecConnections', () => ({
  useCodecConnections: () => ({ connections: [], loading: false }),
  useCodecRouteSpecs: () => ({ specs: [], loading: false }),
}));

const template = {
  id: 'a1',
  name: '登录',
  pattern: 'setState',
  data: { pattern: 'setState' as const },
  createdAt: 1,
  updatedAt: 1,
};

beforeEach(() => {
  vi.clearAllMocks();
  mocks.capability.templateLibrary = true;
  mocks.capability.loading = false;
  mocks.getAction.mockResolvedValue(template);
});

describe('TemplateEditorDrawer', () => {
  it('能力不可用时禁用保存', async () => {
    mocks.capability.templateLibrary = false;
    render(
      <AntApp>
        <TemplateEditorDrawer />
      </AntApp>,
    );
    expect(screen.getByRole('button', { name: /保\s*存/ }).hasAttribute('disabled')).toBe(true);
    expect(await screen.findByText('共享模板库功能未启用，请检查服务器配置')).toBeTruthy();
  });

  it('保存失败时保留编辑值和窗口，并显示统一错误', async () => {
    const failure = new Error('保存失败');
    mocks.updateAction.mockRejectedValue(failure);
    const user = userEvent.setup();
    render(
      <AntApp>
        <TemplateEditorDrawer />
      </AntApp>,
    );
    const name = await screen.findByDisplayValue('登录');
    await user.clear(name);
    await user.type(name, '新登录');
    await user.click(screen.getByRole('button', { name: /保\s*存/ }));

    await waitFor(() => expect(mocks.showApiError).toHaveBeenCalledWith(failure));
    expect(screen.getByDisplayValue('新登录')).toBeTruthy();
    expect(mocks.closePanel).not.toHaveBeenCalled();
  });

  it('切换 pattern 时清掉旧 pattern 的专属字段', async () => {
    mocks.getAction.mockResolvedValue({
      ...template,
      pattern: 'tcpRequest',
      data: {
        pattern: 'tcpRequest',
        service: 'logic',
        route: { cmd: 1 },
        c2sProto: 'LoginC2S',
        s2cProto: 'LoginS2C',
        timeout: 5,
        bindings: [{ type: 'fixed', field: 'id', value: 1 }],
      },
    });
    mocks.updateAction.mockResolvedValue(undefined);
    render(
      <AntApp>
        <TemplateEditorDrawer />
      </AntApp>,
    );

    fireEvent.click(await screen.findByRole('button', { name: '切到 clearState' }));
    fireEvent.click(screen.getByRole('button', { name: /保\s*存/ }));

    // clearState 只允许 pattern + keys，tcpRequest 的连接/协议/绑定字段都不该留下。
    // data 必须用 toEqual 精确比对：toMatchObject 是子集匹配，残留字段照样能通过。
    await waitFor(() => expect(mocks.updateAction).toHaveBeenCalled());
    const saved = mocks.updateAction.mock.calls[0][0];
    expect(saved.pattern).toBe('clearState');
    expect(saved.data).toEqual({ pattern: 'clearState' });
  });
});

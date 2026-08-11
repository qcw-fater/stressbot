import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from './api';
import { setMessageApi, showApiError } from './errorHandler';

const error = vi.fn();

afterEach(() => {
  error.mockReset();
  setMessageApi(null);
});

describe('模板库错误提示', () => {
  it.each([
    ['TEMPLATE_LIBRARY_DISABLED', '共享模板库功能未启用，请检查服务器配置'],
    ['ACTION_TEMPLATE_NOT_FOUND', '动作模板不存在或已被删除'],
    ['LISTEN_TEMPLATE_NOT_FOUND', '监听模板不存在或已被删除'],
    ['TEMPLATE_NAME_CONFLICT', '同类模板名称已存在'],
    ['TEMPLATE_SNAPSHOT_CONFLICT', '模板库已被其他窗口更新，请刷新后重试'],
  ])('%s 显示稳定中文提示', (code, expected) => {
    setMessageApi({ message: { error } as never });

    showApiError(new ApiError({ code, message: 'server detail' }, 409));

    expect(error).toHaveBeenCalledWith(expected);
  });

  it('未知错误码仍显示服务器消息', () => {
    setMessageApi({ message: { error } as never });
    showApiError(new ApiError({ code: 'UNKNOWN_TEMPLATE_ERROR', message: '原始错误' }, 500));
    expect(error).toHaveBeenCalledWith('原始错误');
  });

  it.each(['AbortError', 'CancelledError'])('主动取消 %s 不显示错误提示', (name) => {
    setMessageApi({ message: { error } as never });
    showApiError(Object.assign(new Error('已取消'), { name }));
    expect(error).not.toHaveBeenCalled();
  });
});

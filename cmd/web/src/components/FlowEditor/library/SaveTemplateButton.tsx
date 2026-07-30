/**
 * “加入模板库”按钮：一键保存当前 action/listen 到服务器共享模板库。
 *
 * 模板名默认取 action/listen 名。
 * 同名模板已存在时弹出确认框，确认后覆盖更新。
 */

import { App as AntApp, Button, Tooltip } from 'antd';
import { StarOutlined } from '@ant-design/icons';
import type { ActionDef } from '@/types/action';
import type { ListenDef } from '@/types/listen';
import { classifyListen } from '@/types/listen';
import {
  findActionTemplateByName,
  findListenTemplateByName,
  saveActionTemplate,
  saveListenTemplate,
  updateActionTemplate,
  updateListenTemplate,
} from './templateStore';
import { useFlowStore } from '../store/flowStore';
import { inferListenDefaultRef } from './listenTemplateDefaults';
import { useTemplateLibraryCapability } from './useTemplateLibraryCapability';
import { showApiError } from '@/services/errorHandler';

interface ActionProps {
  kind: 'action';
  name: string;
  data: ActionDef;
  description?: string;
}
interface ListenProps {
  kind: 'listen';
  name: string;
  data: ListenDef;
  description?: string;
}

export function SaveTemplateButton(props: ActionProps | ListenProps) {
  const { message, modal } = AntApp.useApp();
  const { templateLibrary, loading } = useTemplateLibraryCapability();
  const unavailableReason = loading
    ? '正在确认共享模板库状态'
    : templateLibrary === false
      ? '共享模板库功能未启用，请检查服务器配置'
      : undefined;

  const onSave = async () => {
    if (templateLibrary !== true) return;
    if (!props.name) {
      message.warning('请先填写名称');
      return;
    }

    try {
      const existing =
        props.kind === 'action'
          ? await findActionTemplateByName(props.name)
          : await findListenTemplateByName(props.name);

      const doSave = async () => {
        try {
          if (props.kind === 'action') {
            if (existing) {
              await updateActionTemplate({
                ...existing,
                description: props.description,
                pattern: props.data.pattern,
                data: props.data,
              });
            } else {
              await saveActionTemplate({
                name: props.name,
                description: props.description,
                pattern: props.data.pattern,
                data: props.data,
              });
            }
          } else {
            const inferred = inferListenDefaultRef(useFlowStore.getState().nodes, props.name);
            const defaultRef = inferred.defaultRef ?? (existing && 'defaultRef' in existing ? existing.defaultRef : undefined);
            if (existing) {
              await updateListenTemplate({
                ...existing,
                description: props.description,
                kind: classifyListen(props.data),
                data: props.data,
                defaultRef,
              });
            } else {
              await saveListenTemplate({
                name: props.name,
                description: props.description,
                kind: classifyListen(props.data),
                data: props.data,
                defaultRef,
              });
            }
            if (inferred.ambiguous) {
              message.warning('存在多条不同监听注册，已使用第一条作为模板默认注册');
            }
          }
          message.success(`已保存模板 "${props.name}"`);
        } catch (error) {
          showApiError(error);
        }
      };

      if (existing) {
        modal.confirm({
          title: '模板已存在',
          content: `模板 "${props.name}" 已存在，是否覆盖？`,
          okText: '覆盖',
          cancelText: '取消',
          onOk: doSave,
        });
      } else {
        await doSave();
      }
    } catch (error) {
      showApiError(error);
    }
  };

  return (
    <Tooltip title={unavailableReason ?? '一键保存到模板库'}>
      <span>
        <Button
          size="small"
          icon={<StarOutlined />}
          onClick={onSave}
          disabled={templateLibrary !== true}
        >
          加入模板库
        </Button>
      </span>
    </Tooltip>
  );
}

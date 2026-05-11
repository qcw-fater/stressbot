/**
 * "加入模板库"按钮：一键保存当前 action/listen 到 IndexedDB。
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

  const onSave = async () => {
    if (!props.name) {
      message.warning('请先填写名称');
      return;
    }

    const existing =
      props.kind === 'action'
        ? await findActionTemplateByName(props.name)
        : await findListenTemplateByName(props.name);

    const doSave = async () => {
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
        if (existing) {
          await updateListenTemplate({
            ...existing,
            description: props.description,
            kind: classifyListen(props.data),
            data: props.data,
          });
        } else {
          await saveListenTemplate({
            name: props.name,
            description: props.description,
            kind: classifyListen(props.data),
            data: props.data,
          });
        }
      }
      message.success(`已保存模板 "${props.name}"`);
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
  };

  return (
    <Tooltip title="一键保存到模板库">
      <Button
        size="small"
        icon={<StarOutlined />}
        onClick={onSave}
      >
        加入模板库
      </Button>
    </Tooltip>
  );
}

/**
 * "加入模板库"按钮：弹简易表单，把当前 action/callback 存入 IndexedDB。
 */

import { Button, Form, Input, Modal, message } from 'antd';
import { StarOutlined } from '@ant-design/icons';
import { useState } from 'react';
import type { ActionDef } from '@/types/action';
import type { CallbackDef } from '@/types/callback';
import { classifyCallback } from '@/types/callback';
import { saveActionTemplate, saveCallbackTemplate } from './templateStore';

interface ActionProps {
  kind: 'action';
  name: string;
  data: ActionDef;
}
interface CallbackProps {
  kind: 'callback';
  name: string;
  data: CallbackDef;
}

export function SaveTemplateButton(props: ActionProps | CallbackProps) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState(props.name);
  const [desc, setDesc] = useState('');

  const onSave = async () => {
    if (!name) {
      message.error('请输入模板名');
      return;
    }
    if (props.kind === 'action') {
      await saveActionTemplate({
        name,
        description: desc || undefined,
        pattern: props.data.pattern,
        data: props.data,
      });
    } else {
      await saveCallbackTemplate({
        name,
        description: desc || undefined,
        kind: classifyCallback(props.data),
        data: props.data,
      });
    }
    message.success(`已保存模板 "${name}"`);
    setOpen(false);
  };

  return (
    <>
      <Button
        size="small"
        icon={<StarOutlined />}
        onClick={() => {
          setName(props.name);
          setOpen(true);
        }}
      >
        加入模板库
      </Button>
      <Modal
        open={open}
        title="保存到模板库"
        onCancel={() => setOpen(false)}
        onOk={onSave}
        okText="保存"
      >
        <Form layout="vertical">
          <Form.Item label="模板名" required>
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </Form.Item>
          <Form.Item label="描述（可选）">
            <Input.TextArea rows={2} value={desc} onChange={(e) => setDesc(e.target.value)} />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
}

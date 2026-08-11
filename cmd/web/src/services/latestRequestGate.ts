export interface LatestRequestToken {
  readonly id: number;
  readonly target: string;
  readonly signal: AbortSignal;
}

/** 让切换目标的异步读取遵守 latest-wins，并主动取消上一条请求链。 */
export class LatestRequestGate {
  private nextId = 0;
  private current?: { token: LatestRequestToken; controller: AbortController };

  begin(target: string): LatestRequestToken {
    this.current?.controller.abort();
    const controller = new AbortController();
    const token = { id: ++this.nextId, target, signal: controller.signal };
    this.current = { token, controller };
    return token;
  }

  isCurrent(token: LatestRequestToken, target: string): boolean {
    return (
      !token.signal.aborted &&
      this.current?.token.id === token.id &&
      this.current.token.target === target
    );
  }

  cancel(): void {
    this.current?.controller.abort();
    this.current = undefined;
  }
}

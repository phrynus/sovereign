import { registerFragment, useI18n } from "@/lib/i18n";
import { enChat } from "@/lib/i18n/fragments/en.chat";
registerFragment(enChat);

import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { MessageSquare } from "lucide-react";
import { PageContainer } from "@/components/ui/page-container";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { cn, formatTime } from "@/lib/utils";
import {
  useChatWorkspace,
  type ChatChannelView,
  type ChatMessageView,
} from "./useChatWorkspace";

const ENGAGEMENT_STATE_KEYS: Record<string, string> = {
  active: "chat.state.active",
  scheduled: "chat.state.scheduled",
  ended: "chat.state.ended",
  closed: "chat.state.closed",
};

function MessageRow({
  message,
  busy,
  onDecide,
}: {
  message: ChatMessageView;
  busy: boolean;
  onDecide: (taskId: number, approve: boolean) => void;
}) {
  const { t } = useI18n();
  const isApproval = message.kind === "approval" && message.task_id != null;
  const author = message.username || (message.kind === "system" ? t("chat.system") : t("chat.you"));
  return (
    <li data-message-id={message.id} className="rounded-md border border-border/60 bg-muted/20 px-3 py-2">
      <div className="flex flex-wrap items-baseline gap-x-2 text-micro text-muted-foreground">
        <span className="font-medium text-foreground">{author}</span>
        <span>{formatTime(message.created_at)}</span>
        {isApproval && <span className="uppercase">{t("chat.approval")}</span>}
        {message.pending && <span className="text-muted-foreground">{t("chat.sending")}</span>}
      </div>
      {/* Plain text only: chat messages are operator-authored data, never HTML
          or Markdown, and no sanitizer-dependent rendering is needed. */}
      <p className="whitespace-pre-wrap break-words text-compact text-foreground">{message.message}</p>
      {isApproval && (
        <div className="mt-2 flex flex-wrap items-center gap-2" data-testid={`approval-${message.task_id}`}>
          <span className="mono-cell text-micro text-muted-foreground">#{message.task_id}</span>
          <Button
            size="sm"
            disabled={busy}
            onClick={() => onDecide(message.task_id as number, true)}
          >
            {busy ? t("chat.acting") : t("chat.approve")}
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={busy}
            onClick={() => onDecide(message.task_id as number, false)}
          >
            {busy ? t("chat.acting") : t("chat.reject")}
          </Button>
        </div>
      )}
    </li>
  );
}

function ChannelButton({
  channel,
  active,
  onSelect,
}: {
  channel: ChatChannelView;
  active: boolean;
  onSelect: () => void;
}) {
  const { t } = useI18n();
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={active ? "true" : undefined}
      className={cn(
        "flex w-full min-w-0 flex-col gap-0.5 rounded-md border px-2.5 py-2 text-left text-compact transition-colors",
        active ? "border-primary/60 bg-primary/10" : "border-transparent hover:bg-muted/50",
      )}
    >
      <span className="flex w-full min-w-0 items-center justify-between gap-2">
        <span className="truncate font-medium">{channel.name}</span>
        {channel.unread_count > 0 && (
          <span className="mono-cell shrink-0 text-micro text-primary">
            {channel.unread_count} {t("chat.unread")}
          </span>
        )}
      </span>
      <span className="flex flex-wrap gap-2 text-micro text-muted-foreground">
        <span>{channel.kind}</span>
        {channel.muted && <span>{t("chat.mute")}</span>}
        {channel.archived && <span>{t("chat.archived")}</span>}
      </span>
    </button>
  );
}

export default function ChatPage() {
  const { t } = useI18n();
  const chat = useChatWorkspace();
  const transcriptRef = useRef<HTMLOListElement>(null);
  const atBottomRef = useRef(true);
  const [newMessageCount, setNewMessageCount] = useState(0);

  const scrollToBottom = useCallback(() => {
    const transcript = transcriptRef.current;
    atBottomRef.current = true;
    setNewMessageCount(0);
    if (transcript && typeof transcript.scrollTo === "function") {
      transcript.scrollTo({ top: transcript.scrollHeight, behavior: "smooth" });
    }
  }, []);

  useEffect(() => {
    atBottomRef.current = true;
    setNewMessageCount(0);
  }, [chat.channelId, chat.search]);

  useEffect(() => {
    if (atBottomRef.current) {
      scrollToBottom();
    } else {
      setNewMessageCount((n) => n + 1);
    }
  }, [chat.messages.length, scrollToBottom]);
  const { engagementId, channelId, activeChannel } = chat;

  const submitMessage = (event: FormEvent) => {
    event.preventDefault();
    void chat.send();
  };
  const submitSearch = (event: FormEvent) => {
    event.preventDefault();
    chat.applySearch(chat.searchInput);
  };
  const submitChannel = (event: FormEvent) => {
    event.preventDefault();
    void chat.createChannel();
  };

  return (
    <PageContainer
      variant="workspace"
      className="h-full"
      title={t("chat.title")}
      subtitle={t("chat.subtitle")}
      icon={<MessageSquare aria-hidden="true" className="size-5" />}
    >
      {chat.error && (
        <div role="alert" className="flex flex-wrap items-center gap-2 rounded-md border border-destructive/40 px-3 py-2 text-compact text-destructive">
          <span>{chat.error}</span>
          <Button size="sm" variant="outline" onClick={chat.reload}>{t("chat.retry")}</Button>
        </div>
      )}

      <div className="grid min-h-0 flex-1 gap-3 lg:grid-cols-[minmax(160px,220px)_minmax(160px,240px)_minmax(0,1fr)]">
        <section className="flex min-h-0 flex-col gap-2 overflow-y-auto" aria-label={t("chat.engagements")}>
          <h2 className="text-section font-semibold">{t("chat.engagements")}</h2>
          {chat.loadingEngagements && <p className="text-compact text-muted-foreground">{t("chat.loading")}</p>}
          {!chat.loadingEngagements && chat.engagements.length === 0 && (
            <p className="text-compact text-muted-foreground">{t("chat.no_engagements")}</p>
          )}
          {chat.engagements.map((engagement) => (
            <button
              key={engagement.id}
              type="button"
              onClick={() => chat.selectEngagement(engagement.id)}
              aria-current={engagementId === engagement.id ? "true" : undefined}
              className={cn(
                "flex min-w-0 flex-col rounded-md border px-2.5 py-2 text-left text-compact",
                engagementId === engagement.id ? "border-primary/60 bg-primary/10" : "border-transparent hover:bg-muted/50",
              )}
            >
              <span className="truncate font-medium">{engagement.name}</span>
              <span className="text-micro text-muted-foreground">{t(ENGAGEMENT_STATE_KEYS[engagement.state] ?? "chat.state.scheduled")}</span>
            </button>
          ))}
        </section>

        <section className="flex min-h-0 flex-col gap-2 overflow-y-auto" aria-label={t("chat.channels")}>
          <div className="flex items-center justify-between gap-2">
            <h2 className="text-section font-semibold">{t("chat.channels")}</h2>
            {engagementId != null && (
              <Button size="sm" variant="outline" onClick={() => chat.setShowCreate(!chat.showCreate)}>
                {t("chat.create_channel")}
              </Button>
            )}
          </div>
          {chat.showCreate && engagementId != null && (
            <form onSubmit={submitChannel} className="flex flex-col gap-2 rounded-md border p-2">
              <Input
                value={chat.newChannelName}
                onChange={(event) => chat.setNewChannelName(event.target.value)}
                placeholder={t("chat.channel_name")}
                aria-label={t("chat.channel_name")}
                maxLength={64}
              />
              <div className="flex gap-2">
                <Button type="submit" size="sm">{t("chat.create")}</Button>
                <Button type="button" size="sm" variant="ghost" onClick={() => chat.setShowCreate(false)}>
                  {t("chat.cancel")}
                </Button>
              </div>
            </form>
          )}
          {chat.loadingChannels && <p className="text-compact text-muted-foreground">{t("chat.loading")}</p>}
          {!chat.loadingChannels && chat.channels.length === 0 && (
            <p className="text-compact text-muted-foreground">{t("chat.no_channels")}</p>
          )}
          {chat.channels.map((channel) => (
            <ChannelButton
              key={channel.id}
              channel={channel}
              active={channelId === channel.id}
              onSelect={() => chat.selectChannel(channel.id)}
            />
          ))}
        </section>

        <section className="flex min-h-0 flex-col gap-2" aria-label={t("chat.title")}>
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-section font-semibold">{activeChannel?.name ?? t("chat.select_channel")}</h2>
            {activeChannel && (
              <>
                <Button size="sm" variant="outline" onClick={() => void chat.toggleMute(activeChannel)}>
                  {activeChannel.muted ? t("chat.unmute") : t("chat.mute")}
                </Button>
                {!activeChannel.archived && activeChannel.kind === "custom" && (
                  <Button size="sm" variant="outline" onClick={() => void chat.archiveChannel(activeChannel)}>
                    {t("chat.archive")}
                  </Button>
                )}
                <Button size="sm" variant="outline" onClick={() => void chat.markRead()}>
                  {t("chat.mark_read")}
                </Button>
              </>
            )}
          </div>
          {!chat.connected && (
            <div role="status" className="flex flex-wrap items-center gap-2 rounded-md border border-warning/40 px-3 py-2 text-compact text-warning-ink">
              <span>{chat.reconnectFailed ? t("chat.offline") : t("chat.reconnecting")}</span>
              {chat.reconnectFailed && <Button size="sm" variant="outline" onClick={chat.reconnect}>{t("chat.reconnect")}</Button>}
            </div>
          )}

          <form onSubmit={submitSearch} className="flex flex-wrap gap-2">
            <Input
              value={chat.searchInput}
              onChange={(event) => chat.setSearchInput(event.target.value)}
              placeholder={t("chat.search_placeholder")}
              aria-label={t("chat.search_placeholder")}
            />
            <Button type="submit" size="sm" variant="outline">{t("chat.search")}</Button>
            {chat.search && (
              <Button type="button" size="sm" variant="ghost" onClick={() => {
                chat.setSearchInput("");
                chat.applySearch("");
              }}>
                {t("chat.clear_search")}
              </Button>
            )}
          </form>

          <ol
            ref={transcriptRef}
            onScroll={(event) => {
              const el = event.currentTarget;
              atBottomRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
              if (atBottomRef.current) setNewMessageCount(0);
            }}
            role="log"
            aria-live="polite"
            className="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto"
            aria-label={t("chat.title")}
          >
            {newMessageCount > 0 && (
              <li>
                <Button size="sm" variant="outline" onClick={scrollToBottom}>
                  {t("chat.new_messages", { count: newMessageCount })}
                </Button>
              </li>
            )}
            {chat.nextBeforeId != null && (
              <li>
                <Button size="sm" variant="ghost" onClick={() => void chat.loadOlder()}>
                  {t("chat.load_older")}
                </Button>
              </li>
            )}
            {chat.loadingHistory && <li className="text-compact text-muted-foreground">{t("chat.loading")}</li>}
            {!chat.loadingHistory && chat.messages.length === 0 && (
              <li className="text-compact text-muted-foreground">
                {channelId == null ? t("chat.select_channel") : t("chat.no_messages")}
              </li>
            )}
            {chat.messages.map((message) => (
              <MessageRow
                key={message.id}
                message={message}
                busy={chat.busyTaskId === message.task_id && message.task_id != null}
                onDecide={(taskId, approve) => void chat.decideTask(taskId, approve)}
              />
            ))}
          </ol>

          <form onSubmit={submitMessage} className="flex flex-wrap gap-2 border-t border-border/60 pt-2">
            <Textarea
              value={chat.draft}
              onChange={(event) => chat.setDraft(event.target.value)}
              placeholder={t("chat.message_placeholder")}
              aria-label={t("chat.message_placeholder")}
              rows={2}
              className="min-w-0 flex-1 resize-y"
            />
            <Button type="submit" disabled={chat.sending || channelId == null || !chat.draft.trim()}>
              {chat.sending ? t("chat.sending") : t("chat.send")}
            </Button>
          </form>
        </section>
      </div>
    </PageContainer>
  );
}

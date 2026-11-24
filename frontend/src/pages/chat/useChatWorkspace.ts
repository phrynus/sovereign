import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { asRecord, firstArray } from "@/lib/envelope";
import { errorText } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { useTypedWS } from "@/lib/typed-ws";
import { useWebSocket } from "@/lib/useWebSocket";

const HISTORY_LIMIT = 50;

export interface EngagementOption {
  id: number;
  name: string;
  state: string;
}

export interface ChatChannelView {
  id: number;
  name: string;
  slug: string;
  kind: string;
  archived: boolean;
  muted: boolean;
  unread_count: number;
}

export interface ChatMessageView {
  id: number;
  engagement_id: number | null;
  channel_id: number | null;
  user_id: number | null;
  username: string;
  message: string;
  kind: string;
  task_id: number | null;
  metadata: string;
  created_at: string;
}

function toId(value: unknown): number | null {
  if (value == null || value === "") return null;
  const n = typeof value === "number" ? value : Number(value);
  return Number.isSafeInteger(n) && n > 0 ? n : null;
}

function parseEngagement(raw: unknown): EngagementOption | null {
  const row = asRecord(raw);
  const id = toId(row?.id);
  if (row == null || id == null) return null;
  const state = typeof row.state === "string" && row.state.trim() ? row.state : "scheduled";
  return { id, name: typeof row.name === "string" ? row.name : `#${id}`, state };
}

function parseChannel(raw: unknown): ChatChannelView | null {
  const row = asRecord(raw);
  const id = toId(row?.id);
  if (row == null || id == null) return null;
  return {
    id,
    name: typeof row.name === "string" ? row.name : typeof row.slug === "string" ? row.slug : `#${id}`,
    slug: typeof row.slug === "string" ? row.slug : typeof row.channel === "string" ? row.channel : "",
    kind: typeof row.kind === "string" ? row.kind : "custom",
    archived: row.archived === true || row.archived_at != null,
    muted: row.muted === true,
    unread_count: typeof row.unread_count === "number" && Number.isFinite(row.unread_count) ? row.unread_count : 0,
  };
}

function parseMessage(raw: unknown): ChatMessageView | null {
  const row = asRecord(raw);
  const id = toId(row?.id);
  if (row == null || id == null) return null;
  return {
    id,
    engagement_id: toId(row.engagement_id),
    channel_id: toId(row.channel_id),
    user_id: toId(row.user_id),
    username: typeof row.username === "string" ? row.username : "",
    message: typeof row.message === "string" ? row.message : "",
    kind: typeof row.kind === "string" ? row.kind : "user",
    task_id: toId(row.task_id),
    metadata: typeof row.metadata === "string" ? row.metadata : "",
    created_at: typeof row.created_at === "string" ? row.created_at : "",
  };
}

/** History arrives newest-first; the transcript reads oldest-first. */
function sortMessages(rows: ChatMessageView[]): ChatMessageView[] {
  return [...rows].sort((a, b) =>
    a.created_at.localeCompare(b.created_at) || a.id - b.id,
  );
}

function mergeMessages(
  current: ChatMessageView[],
  incoming: ChatMessageView[],
  replace: boolean,
): ChatMessageView[] {
  if (replace) return sortMessages(incoming);
  const byId = new Map(current.map((row) => [row.id, row]));
  for (const row of incoming) byId.set(row.id, row);
  return sortMessages([...byId.values()]);
}

/**
 * Engagement/channel state for /chat. REST is the source of truth for history
 * and room lists; the WebSocket only contributes live frames. A frame or a
 * reconnect refresh can never duplicate a message because every merge is keyed
 * by the server-assigned message id.
 */
export function useChatWorkspace() {
  const { t } = useI18n();
  const tRef = useRef(t);
  tRef.current = t;
  const { connected } = useWebSocket();

  const [engagements, setEngagements] = useState<EngagementOption[]>([]);
  const [engagementId, setEngagementId] = useState<number | null>(null);
  const [channels, setChannels] = useState<ChatChannelView[]>([]);
  const [channelId, setChannelId] = useState<number | null>(null);
  const [messages, setMessages] = useState<ChatMessageView[]>([]);
  const [nextBeforeId, setNextBeforeId] = useState<number | null>(null);
  const [searchInput, setSearchInput] = useState("");
  const [search, setSearch] = useState("");
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loadingEngagements, setLoadingEngagements] = useState(true);
  const [loadingChannels, setLoadingChannels] = useState(false);
  const [loadingHistory, setLoadingHistory] = useState(false);
  const [sending, setSending] = useState(false);
  const [busyTaskId, setBusyTaskId] = useState<number | null>(null);
  const [showCreate, setShowCreate] = useState(false);
  const [newChannelName, setNewChannelName] = useState("");
  const [reconnectToken, setReconnectToken] = useState(0);

  const engagementSeq = useRef(0);
  const channelSeq = useRef(0);
  const historySeq = useRef(0);
  const engagementIdRef = useRef(engagementId);
  const channelIdRef = useRef(channelId);
  const messagesRef = useRef(messages);
  const loadedKeyRef = useRef("");
  const historyLoadedRef = useRef(false);
  const previousConnectedRef = useRef(connected);
  const readWatermarkRef = useRef("");
  engagementIdRef.current = engagementId;
  channelIdRef.current = channelId;
  messagesRef.current = messages;

  const fail = useCallback((err: unknown, fallback: string) => {
    setError(errorText(tRef.current, err, () => fallback));
  }, []);

  const loadEngagements = useCallback(async () => {
    const seq = ++engagementSeq.current;
    setLoadingEngagements(true);
    setError(null);
    try {
      const data = await api.get(paths.engagements.list);
      if (seq !== engagementSeq.current) return;
      const rows = firstArray(data, ["engagements", "data"])
        .map(parseEngagement)
        .filter((row): row is EngagementOption => row != null);
      setEngagements(rows);
      setEngagementId((prev) =>
        prev != null && rows.some((row) => row.id === prev) ? prev : rows[0]?.id ?? null,
      );
    } catch (err) {
      if (seq === engagementSeq.current) fail(err, tRef.current("chat.error"));
    } finally {
      if (seq === engagementSeq.current) setLoadingEngagements(false);
    }
  }, [fail]);

  useEffect(() => {
    void loadEngagements();
    return () => { engagementSeq.current++; };
  }, [loadEngagements]);

  const loadChannels = useCallback(async (targetEngagement: number, preferredId?: number | null) => {
    const seq = ++channelSeq.current;
    setLoadingChannels(true);
    setError(null);
    try {
      const data = await api.get(paths.scopedChat.channels(targetEngagement));
      if (seq !== channelSeq.current || targetEngagement !== engagementIdRef.current) return;
      const rows = firstArray(data, ["channels", "data"])
        .map(parseChannel)
        .filter((row): row is ChatChannelView => row != null);
      setChannels(rows);
      setChannelId((prev) => {
        if (preferredId != null && rows.some((row) => row.id === preferredId)) return preferredId;
        if (prev != null && rows.some((row) => row.id === prev)) return prev;
        return rows.find((row) => row.slug === "general")?.id ?? rows[0]?.id ?? null;
      });
    } catch (err) {
      if (seq === channelSeq.current) fail(err, tRef.current("chat.error"));
    } finally {
      if (seq === channelSeq.current) setLoadingChannels(false);
    }
  }, [fail]);

  useEffect(() => {
    if (engagementId == null) {
      setChannels([]);
      setChannelId(null);
      return;
    }
    void loadChannels(engagementId);
    return () => { channelSeq.current++; };
  }, [engagementId, loadChannels]);

  const loadHistory = useCallback(async (beforeId?: number, merge = false) => {
    const targetEngagement = engagementIdRef.current;
    const targetChannel = channelIdRef.current;
    if (targetEngagement == null || targetChannel == null) return;
    const seq = ++historySeq.current;
    setLoadingHistory(true);
    setError(null);
    try {
      let url = paths.scopedChat.history(targetEngagement, targetChannel) + `&limit=${HISTORY_LIMIT}`;
      if (beforeId != null) url += `&before_id=${beforeId}`;
      if (search.trim()) url += `&q=${encodeURIComponent(search.trim())}`;
      const data = await api.get(url);
      if (seq !== historySeq.current) return;
      const rows = firstArray(data, ["messages", "data"])
        .map(parseMessage)
        .filter((row): row is ChatMessageView => row != null);
      setMessages((prev) => mergeMessages(prev, rows, !merge));
      setNextBeforeId(toId(asRecord(data)?.next_before_id));
      historyLoadedRef.current = true;
    } catch (err) {
      if (seq === historySeq.current) fail(err, tRef.current("chat.error"));
    } finally {
      if (seq === historySeq.current) setLoadingHistory(false);
    }
  }, [fail, search]);

  useEffect(() => {
    const key = `${engagementId ?? ""}:${channelId ?? ""}:${search}`;
    const switched = loadedKeyRef.current !== key;
    loadedKeyRef.current = key;
    if (switched) {
      // Never let a previous engagement/channel's transcript bleed into the
      // newly selected room while the next request is in flight.
      setMessages([]);
      setNextBeforeId(null);
      historyLoadedRef.current = false;
      readWatermarkRef.current = "";
    }
    if (engagementId == null || channelId == null) return;
    void loadHistory(undefined, !switched);
    return () => { historySeq.current++; };
  }, [engagementId, channelId, loadHistory, reconnectToken]);

  useEffect(() => {
    const wasConnected = previousConnectedRef.current;
    previousConnectedRef.current = connected;
    if (connected && !wasConnected && historyLoadedRef.current) setReconnectToken((n) => n + 1);
  }, [connected]);

  useTypedWS("chat", (frame) => {
    const frameEngagement = toId(frame.engagement_id);
    const frameChannel = toId(frame.channel_id);
    const id = toId(frame.id);
    if (id == null || frameEngagement == null || frameChannel == null) return;
    if (frameEngagement !== engagementIdRef.current || frameChannel !== channelIdRef.current) return;
    const incoming: ChatMessageView = {
      id,
      engagement_id: frameEngagement,
      channel_id: frameChannel,
      user_id: toId(frame.user_id),
      username: typeof frame.username === "string" ? frame.username : "",
      message: typeof frame.message === "string" ? frame.message : "",
      kind: typeof frame.kind === "string" ? frame.kind : "user",
      task_id: toId(frame.task_id),
      metadata: typeof frame.metadata === "string" ? frame.metadata : "",
      created_at: typeof frame.created_at === "string" ? frame.created_at : new Date().toISOString(),
    };
    setMessages((prev) => mergeMessages(prev, [incoming], false));
  });

  const selectEngagement = useCallback((id: number) => {
    if (id === engagementIdRef.current) return;
    channelSeq.current++;
    historySeq.current++;
    engagementIdRef.current = id;
    setEngagementId(id);
    setChannels([]);
    setChannelId(null);
    setMessages([]);
    setNextBeforeId(null);
    setError(null);
  }, []);

  const selectChannel = useCallback((id: number) => {
    if (id === channelIdRef.current) return;
    historySeq.current++;
    channelIdRef.current = id;
    setChannelId(id);
    setMessages([]);
    setNextBeforeId(null);
    setError(null);
  }, []);

  const loadOlder = useCallback(async () => {
    if (nextBeforeId == null) return;
    await loadHistory(nextBeforeId, true);
  }, [loadHistory, nextBeforeId]);

  const applySearch = useCallback((value: string) => {
    setSearch(value.trim());
  }, []);

  const markRead = useCallback(async (targetId?: number) => {
    const currentChannel = channelIdRef.current;
    const latest = targetId ?? messagesRef.current.reduce((max, row) => Math.max(max, row.id), 0);
    if (currentChannel == null || latest === 0) return;
    try {
      await api.postJson(paths.scopedChat.read(currentChannel), { message_id: latest });
      setChannels((prev) => prev.map((row) => row.id === currentChannel ? { ...row, unread_count: 0 } : row));
    } catch (err) {
      fail(err, tRef.current("chat.error"));
    }
  }, [fail]);

  useEffect(() => {
    if (engagementId == null || channelId == null || search.trim()) return;
    const active = channels.find((row) => row.id === channelId);
    if (!active || active.unread_count <= 0 || messages.length === 0) return;
    const latest = messages.reduce((max, row) => Math.max(max, row.id), 0);
    const key = `${channelId}:${latest}`;
    if (readWatermarkRef.current === key) return;
    readWatermarkRef.current = key;
    void markRead(latest);
  }, [channels, channelId, engagementId, messages, markRead, search]);

  const send = useCallback(async () => {
    const text = draft.trim();
    const targetEngagement = engagementIdRef.current;
    const targetChannel = channelIdRef.current;
    if (!text || targetEngagement == null || targetChannel == null || sending) return;
    setSending(true);
    setError(null);
    try {
      await api.postJson(paths.scopedChat.send, {
        engagement_id: targetEngagement,
        channel_id: targetChannel,
        message: text,
      });
      setDraft("");
      await loadHistory(undefined, true);
    } catch (err) {
      fail(err, tRef.current("chat.error"));
    } finally {
      setSending(false);
    }
  }, [draft, fail, loadHistory, sending]);

  const createChannel = useCallback(async () => {
    const targetEngagement = engagementIdRef.current;
    const name = newChannelName.trim();
    if (targetEngagement == null || !name) return;
    setError(null);
    try {
      const data = await api.postJson(paths.scopedChat.createChannel, {
        engagement_id: targetEngagement,
        name,
      });
      const created = asRecord(asRecord(data)?.channel) ?? asRecord(data);
      const id = toId(created?.id);
      setNewChannelName("");
      setShowCreate(false);
      await loadChannels(targetEngagement, id);
    } catch (err) {
      fail(err, tRef.current("chat.error"));
    }
  }, [fail, loadChannels, newChannelName]);

  const toggleMute = useCallback(async (channel: ChatChannelView) => {
    if (engagementIdRef.current == null) return;
    setError(null);
    try {
      await api.postJson(paths.scopedChat.mute(channel.id), { muted: !channel.muted });
      await loadChannels(engagementIdRef.current);
    } catch (err) {
      fail(err, tRef.current("chat.error"));
    }
  }, [fail, loadChannels]);

  const archiveChannel = useCallback(async (channel: ChatChannelView) => {
    if (engagementIdRef.current == null || channel.archived) return;
    setError(null);
    try {
      await api.postJson(paths.scopedChat.archive(channel.id), {});
      if (channelIdRef.current === channel.id) {
        historySeq.current++;
        channelIdRef.current = null;
        setChannelId(null);
        setMessages([]);
        setNextBeforeId(null);
      }
      await loadChannels(engagementIdRef.current);
    } catch (err) {
      fail(err, tRef.current("chat.error"));
    }
  }, [fail, loadChannels]);

  const decideTask = useCallback(async (taskId: number, approve: boolean) => {
    setBusyTaskId(taskId);
    setError(null);
    try {
      await api.post(approve ? paths.tasksCollab.approve(taskId) : paths.tasksCollab.reject(taskId));
      await loadHistory(undefined, true);
    } catch (err) {
      fail(err, tRef.current("chat.error"));
    } finally {
      setBusyTaskId(null);
    }
  }, [fail, loadHistory]);

  const reload = useCallback(() => {
    void loadEngagements();
    if (engagementIdRef.current != null) void loadChannels(engagementIdRef.current);
    void loadHistory(undefined, true);
  }, [loadChannels, loadEngagements, loadHistory]);

  const activeChannel = channels.find((row) => row.id === channelId) ?? null;

  return {
    engagements, engagementId, selectEngagement,
    channels, channelId, activeChannel, selectChannel,
    messages, nextBeforeId, loadOlder,
    searchInput, setSearchInput, search, applySearch,
    draft, setDraft, send, sending,
    error, reload,
    loadingEngagements, loadingChannels, loadingHistory,
    showCreate, setShowCreate, newChannelName, setNewChannelName, createChannel,
    toggleMute, archiveChannel, markRead,
    busyTaskId, decideTask,
    connected,
  };
}

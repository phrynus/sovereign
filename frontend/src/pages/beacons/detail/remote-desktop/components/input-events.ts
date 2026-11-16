/**
 * Pure translation from browser input events to the agent's remote-input
 * contract. Kept out of the hook so the mapping (which keys exist, when a
 * modifier is held, how a wheel pixel delta becomes a notch) is testable
 * without a DOM.
 *
 * The wire types mirror the Go server (`remoteInputRequest.normalize`):
 *   move | click | click_right | click_middle | wheel | key
 */

export type RemoteModifier = "ctrl" | "alt" | "shift" | "meta";

export type RemotePointerEvent = {
  type: "move" | "click" | "click_right" | "click_middle" | "wheel";
  x: number;
  y: number;
  count?: number;
  delta?: number;
  mods?: RemoteModifier[];
};

export type RemoteKeyEvent = {
  type: "key";
  key: string;
  mods?: RemoteModifier[];
};

export type RemoteInputEvent = RemotePointerEvent | RemoteKeyEvent;

interface ModifierState {
  ctrlKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
  metaKey: boolean;
}

/** Canonical modifier list for one browser event. */
export function modsFromEvent(e: ModifierState): RemoteModifier[] {
  const mods: RemoteModifier[] = [];
  if (e.ctrlKey) mods.push("ctrl");
  if (e.altKey) mods.push("alt");
  if (e.shiftKey) mods.push("shift");
  if (e.metaKey) mods.push("meta");
  return mods;
}

/**
 * Keys that are only modifiers. They are not sent on their own: the agent holds
 * modifiers around a key/glyph, so a bare "Control" press would be an unrelated
 * keypress on the target. The browser event that carries the real key already
 * includes ctrlKey/shiftKey.
 */
const MODIFIER_KEYS = new Set(["Shift", "Control", "Alt", "Meta", "CapsLock", "AltGraph"]);

/** Browser key name → the name the agent understands. */
const KEY_NAMES: Record<string, string> = {
  " ": "space",
  Escape: "esc",
  Enter: "enter",
  Tab: "tab",
  Backspace: "backspace",
  Delete: "delete",
  Insert: "insert",
  Home: "home",
  End: "end",
  PageUp: "pageup",
  PageDown: "pagedown",
  ArrowUp: "up",
  ArrowDown: "down",
  ArrowLeft: "left",
  ArrowRight: "right",
  CapsLock: "capslock",
  PrintScreen: "printscreen",
  Pause: "pause",
};

/**
 * Maps one keydown to the agent's key name, or null when the event carries no
 * injectable key (a modifier, a dead key, an IME composition, or a media key).
 *
 * A single printable character is passed through as-is: the agent resolves it
 * through the target's own keyboard layout (VkKeyScanW / xdotool keysym), so
 * "!" or "/" work without a hard-coded US table. Uppercase letters are sent
 * lowercased with shift in the modifier list, because the agent treats an
 * explicit "A" as shift+0x41 anyway - one representation, not two.
 */
export function keyFromEvent(e: { key: string }): string | null {
  const raw = e.key;
  if (!raw || MODIFIER_KEYS.has(raw)) return null;
  const named = KEY_NAMES[raw];
  if (named) return named;
  if (/^F([1-9]|1[0-2])$/.test(raw)) return raw.toLowerCase();
  if (raw === "Dead" || raw === "Unidentified" || raw === "Process") return null;
  const chars = Array.from(raw);
  if (chars.length !== 1) return null;
  const ch = chars[0];
  const code = ch.charCodeAt(0);
  // Control characters (including "\r"/"\n" already mapped above) and
  // non-ASCII glyphs: a CJK character is not a keystroke on a US/EU target,
  // and the agent would reject it. Only printable ASCII can be injected.
  if (code < 0x20 || code > 0x7e) return null;
  return ch.toLowerCase();
}

/** One wheel notch is this many CSS pixels of deltaY on a typical browser. */
export const WHEEL_PIXELS_PER_NOTCH = 100;
/** Server-side bound (MaxRemoteInputWheelDelta); the console must never exceed it. */
export const MAX_WHEEL_NOTCHES = 10;

/**
 * Converts a wheel event's delta into whole notches. The sign follows the
 * agent contract (positive = away from the user / scroll up), which is the
 * OPPOSITE of the browser's `deltaY` (positive = content scrolls up = the user
 * scrolled down), so the value is negated. The magnitude is bounded so one fast
 * flick cannot ask the agent for a 200-notch scroll.
 */
export function wheelNotches(deltaY: number, deltaMode = 0): number {
  // deltaMode 1 is lines, 2 is pages; both are already "notch-like".
  const scale = deltaMode === 0 ? WHEEL_PIXELS_PER_NOTCH : 1;
  const raw = -deltaY / scale;
  if (!Number.isFinite(raw)) return 0;
  const rounded = raw > 0 ? Math.ceil(raw) : Math.floor(raw);
  if (rounded === 0) return 0;
  return Math.max(-MAX_WHEEL_NOTCHES, Math.min(MAX_WHEEL_NOTCHES, rounded));
}

/** Click count from a DOM click (1 single, 2 double, 3 triple), bounded by the server. */
export function clickCount(detail: number | undefined): number {
  if (!Number.isFinite(detail) || !detail || detail < 1) return 1;
  return Math.min(3, Math.floor(detail));
}

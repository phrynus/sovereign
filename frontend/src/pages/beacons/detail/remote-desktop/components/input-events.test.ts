import { describe, expect, it } from "vitest";
import {
  MAX_WHEEL_NOTCHES,
  clickCount,
  keyFromEvent,
  modsFromEvent,
  wheelNotches,
} from "./input-events";

describe("modsFromEvent", () => {
  it("maps held modifiers to the canonical names", () => {
    expect(modsFromEvent({ ctrlKey: true, altKey: true, shiftKey: true, metaKey: true })).toEqual([
      "ctrl",
      "alt",
      "shift",
      "meta",
    ]);
  });

  it("returns an empty list when nothing is held", () => {
    expect(modsFromEvent({ ctrlKey: false, altKey: false, shiftKey: false, metaKey: false })).toEqual([]);
  });
});

describe("keyFromEvent", () => {
  it("sends printable ASCII as-is, lowercased (shift travels in mods)", () => {
    expect(keyFromEvent({ key: "a" })).toBe("a");
    expect(keyFromEvent({ key: "A" })).toBe("a");
    expect(keyFromEvent({ key: "." })).toBe(".");
    expect(keyFromEvent({ key: "/" })).toBe("/");
    expect(keyFromEvent({ key: "!" })).toBe("!");
  });

  it("maps browser key names onto the agent's names", () => {
    expect(keyFromEvent({ key: " " })).toBe("space");
    expect(keyFromEvent({ key: "Enter" })).toBe("enter");
    expect(keyFromEvent({ key: "Escape" })).toBe("esc");
    expect(keyFromEvent({ key: "Backspace" })).toBe("backspace");
    expect(keyFromEvent({ key: "ArrowUp" })).toBe("up");
    expect(keyFromEvent({ key: "PageDown" })).toBe("pagedown");
    expect(keyFromEvent({ key: "F5" })).toBe("f5");
    expect(keyFromEvent({ key: "F12" })).toBe("f12");
  });

  it("never sends a bare modifier, a dead key, or an uninjectable glyph", () => {
    for (const key of ["Shift", "Control", "Alt", "Meta", "CapsLock", "Dead", "Unidentified", "降", "🙂"]) {
      expect(keyFromEvent({ key }), key).toBeNull();
    }
  });
});

describe("wheelNotches", () => {
  it("maps the browser sign convention onto the agent's (positive = up/away)", () => {
    // deltaY > 0 is "the user scrolled down".
    expect(wheelNotches(100)).toBe(-1);
    expect(wheelNotches(-100)).toBe(1);
    expect(wheelNotches(-250)).toBe(3);
    expect(wheelNotches(250)).toBe(-3);
  });

  it("treats line/page deltas as notches and ignores empty ones", () => {
    expect(wheelNotches(3, 1)).toBe(-3);
    expect(wheelNotches(-1, 2)).toBe(1);
    expect(wheelNotches(0)).toBe(0);
    expect(wheelNotches(Number.NaN)).toBe(0);
  });

  it("bounds one event so a fast flick cannot ask for a 200-notch scroll", () => {
    expect(wheelNotches(-2500)).toBe(MAX_WHEEL_NOTCHES);
    expect(wheelNotches(2500)).toBe(-MAX_WHEEL_NOTCHES);
  });
});

describe("clickCount", () => {
  it("maps the DOM click detail onto 1-3 clicks", () => {
    expect(clickCount(undefined)).toBe(1);
    expect(clickCount(0)).toBe(1);
    expect(clickCount(1)).toBe(1);
    expect(clickCount(2)).toBe(2);
    expect(clickCount(3)).toBe(3);
    expect(clickCount(9)).toBe(3);
  });
});

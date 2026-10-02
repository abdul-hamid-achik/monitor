import { describe, expect, test } from "bun:test";
import { bytes, count, duration, issueBadge, location, percent, shellQuote, shortID, timeAgo } from "../renderer/src/format";

describe("format", () => {
  const now = Date.parse("2026-10-01T12:00:00Z");

  test("timeAgo", () => {
    expect(timeAgo("2026-10-01T11:59:50Z", now)).toBe("just now");
    expect(timeAgo("2026-10-01T11:30:00Z", now)).toBe("30m ago");
    expect(timeAgo("2026-10-01T02:00:00Z", now)).toBe("10h ago");
    expect(timeAgo("2026-09-30T12:00:00Z", now)).toBe("1d ago");
    expect(timeAgo("2026-09-20T12:00:00Z", now)).toBe("11d ago");
    expect(timeAgo(undefined, now)).toBe("—");
    expect(timeAgo("garbage", now)).toBe("—");
  });

  test("bytes, percent, count, duration", () => {
    expect(bytes(0)).toBe("0 B");
    expect(bytes(1536)).toBe("1.5 KB");
    expect(bytes(16 * 1024 ** 3)).toBe("16.0 GB");
    expect(percent(12.345, 1)).toBe("12.3%");
    expect(count(950)).toBe("950");
    expect(count(12_300)).toBe("12k");
    expect(duration(3 * 86400 + 5 * 3600)).toBe("3d 5h");
  });

  test("location and shortID", () => {
    expect(location({ file: "src/a.js", line: 4 })).toBe("src/a.js:4");
    expect(location({})).toBe("");
    expect(shortID("ISS-9FBE04FC5C81F656")).toBe("9FBE");
  });

  test("shellQuote quotes only when needed", () => {
    expect(shellQuote("src/app.js:42")).toBe("src/app.js:42");
    expect(shellQuote("my file.js")).toBe("'my file.js'");
    expect(shellQuote("it's")).toBe("'it'\\''s'");
  });

  test("issueBadge", () => {
    expect(issueBadge({ status: "open", first_seen: "2026-10-01T11:00:00Z", reopened_count: 0 }, now)).toBe("new");
    expect(issueBadge({ status: "open", first_seen: "2026-09-01T11:00:00Z", reopened_count: 2 }, now)).toBe("regressed");
    expect(issueBadge({ status: "open", first_seen: "2026-09-01T11:00:00Z", reopened_count: 0 }, now)).toBeNull();
    expect(issueBadge({ status: "resolved", first_seen: "2026-10-01T11:00:00Z", reopened_count: 0 }, now)).toBe("resolved");
  });
});

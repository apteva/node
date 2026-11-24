import { expect, test } from "bun:test";
import { format, select, template } from "./surfaces.ts";

test("select follows property-only selectors", () => {
  const body = { counts: { active: 3 }, list: [1, 2] };
  expect(select(body, "$")).toBe(body);
  expect(select(body, "$.counts.active")).toBe(3);
  expect(select(body, "$.list")).toEqual([1, 2]);
  expect(select(body, "$.missing.deep")).toBeUndefined();
  expect(select(body, "counts")).toBeUndefined(); // not a selector
});

test("template interpolates known namespaces only", () => {
  const scope = { item: { id: 7, title: "Reorder" }, settings: { n: 4 } };
  expect(template("/tasks/{item.id}", scope)).toBe("/tasks/7");
  expect(template("{item.title} ({settings.n})", scope)).toBe("Reorder (4)");
  expect(template("{item.nope}", scope)).toBe("");
  expect(template("{window.location}", scope)).toBe("{window.location}");
});

test("format is semantic", () => {
  expect(format(1234.5, "currency")).toMatch(/1.234.50|1,234\.50/);
  expect(format(["a", "b"], "tags")).toBe("a · b");
  expect(format(2048, "file_size")).toBe("2.0 KB");
  expect(format(null, "number")).toBe("");
  expect(format("x", "unknown")).toBe("x");
});

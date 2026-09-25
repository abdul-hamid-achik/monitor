function heavyStringify(n) {
  let s = "";
  for (let i = 0; i < n; i++) {
    const o = { a: i, b: "x".repeat(10) };
    s += JSON.stringify(o);           // line 5 hot
    if (s.length > 100000) s = "";
  }
  return s.length;
}
function cheap(n) { let t = 0; for (let i = 0; i < n; i++) t += i; return t; }
const end = Date.now() + 1500;
while (Date.now() < end) { heavyStringify(2000); cheap(1000); }

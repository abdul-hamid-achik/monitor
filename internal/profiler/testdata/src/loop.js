function heavyStringify(n) {
  let s = "";
  for (let i = 0; i < n; i++) {
    const o = { a: i, b: "x".repeat(10) };
    s += JSON.stringify(o);           // line 5 hot
    if (s.length > 100000) s = "";
  }
  return s.length;
}
function tick() { heavyStringify(3000); setTimeout(tick, 1); }
tick();
setTimeout(() => process.exit(0), 60000);

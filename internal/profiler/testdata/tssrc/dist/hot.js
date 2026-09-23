// hot.ts
function heavyWork(n) {
  let total = 0;
  for (let i = 0;i < n; i++) {
    const s = JSON.stringify({ i, pad: "x".repeat(64) });
    total += s.length;
  }
  return total;
}
var deadline = Date.now() + 1200;
while (Date.now() < deadline) {
  heavyWork(3000);
}

//# debugId=11B6DFFC7A2F446D64756E2164756E21

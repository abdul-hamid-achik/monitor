function heavyWork(n: number): number {
  let total = 0;
  for (let i = 0; i < n; i++) {
    const s = JSON.stringify({ i, pad: "x".repeat(64) }); // HOT LINE
    total += s.length;
  }
  return total;
}

const deadline = Date.now() + 1200;
while (Date.now() < deadline) {
  heavyWork(3000);
}

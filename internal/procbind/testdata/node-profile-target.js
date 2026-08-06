// A deliberately CPU-active Node process used for real inspector smoke tests.
// Keep this dependency-free so it can run anywhere Monitor's Node support is tested.
let checksum = 0;

setInterval(() => {
  const deadline = Date.now() + 25;
  while (Date.now() < deadline) {
    checksum = (checksum + Math.imul(checksum + 1, 2654435761)) >>> 0;
  }
}, 50);

process.on("SIGTERM", () => {
  process.stdout.write(`${checksum}\n`);
  process.exit(0);
});

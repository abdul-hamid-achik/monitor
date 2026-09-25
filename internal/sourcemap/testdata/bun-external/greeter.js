// greeter.ts
function greet(g) {
  const parts = [];
  for (let i = 0;i < g.times; i++) {
    parts.push(`Hello, ${g.name}!`);
  }
  return parts.join(" ");
}
function main() {
  const result = greet({ name: "World", times: 3 });
  console.log(result);
}
main();
export {
  greet
};

//# debugId=E9869E90A3791BA764756E2164756E21

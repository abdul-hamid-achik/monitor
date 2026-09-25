export interface Greeting {
  name: string;
  times: number;
}

export function greet(g: Greeting): string {
  const parts: string[] = [];
  for (let i = 0; i < g.times; i++) {
    parts.push(`Hello, ${g.name}!`);
  }
  return parts.join(" ");
}

function main(): void {
  const result = greet({ name: "World", times: 3 });
  console.log(result);
}

main();

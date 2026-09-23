"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.greet = greet;
function greet(g) {
    const parts = [];
    for (let i = 0; i < g.times; i++) {
        parts.push(`Hello, ${g.name}!`);
    }
    return parts.join(" ");
}
function main() {
    const result = greet({ name: "World", times: 3 });
    console.log(result);
}
main();
//# sourceMappingURL=greeter.js.map